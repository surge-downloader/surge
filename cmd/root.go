package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/surge-downloader/surge/internal/config"
	"github.com/surge-downloader/surge/internal/download"
	"github.com/surge-downloader/surge/internal/messages"
	"github.com/surge-downloader/surge/internal/tui"
	"github.com/surge-downloader/surge/internal/utils"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// Version information - set via ldflags during build
var (
	Version   = "dev"
	BuildTime = "unknown"
)

// serverProgram holds the TUI program for sending messages from HTTP handler
var serverProgram *tea.Program

// activeDownloads tracks the number of currently running downloads in headless mode
var activeDownloads int32

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:     "surge",
	Short:   "An open-source download manager written in Go",
	Long:    `Surge is a blazing fast, open-source terminal (TUI) download manager built in Go.`,
	Version: Version,
	Run: func(cmd *cobra.Command, args []string) {
		// Attempt to acquire lock
		isMaster, err := AcquireLock()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error acquiring lock: %v\n", err)
			os.Exit(1)
		}

		if !isMaster {
			fmt.Fprintln(os.Stderr, "Error: Surge is already running.")
			fmt.Fprintln(os.Stderr, "Use 'surge get <url>' to add a download to the active instance.")
			os.Exit(1)
		}
		defer ReleaseLock()

		headless, _ := cmd.Flags().GetBool("headless")
		portFlag, _ := cmd.Flags().GetInt("port")

		var port int
		var listener net.Listener
		// var err error // err already declared above

		if portFlag > 0 {
			// Strict port mode
			port = portFlag
			listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: could not bind to port %d: %v\n", port, err)
				os.Exit(1)
			}
		} else {
			// Auto-discovery mode
			port, listener = findAvailablePort(8080)
			if listener == nil {
				fmt.Fprintf(os.Stderr, "Error: could not find available port\n")
				os.Exit(1)
			}
		}

		// Save port for browser extension AND CLI discovery
		saveActivePort(port)

		outputDir, _ := cmd.Flags().GetString("output")

		// Start HTTP server in background (reuse the listener)
		go startHTTPServer(listener, port, outputDir)

		if headless {
			fmt.Printf("Surge %s running in headless mode.\n", Version)
			fmt.Printf("HTTP server listening on port %d\n", port)
			fmt.Println("Press Ctrl+C to exit.")

			// Block until signal
			sigChan := make(chan os.Signal, 1)
			signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
			<-sigChan

			fmt.Println("\nShutting down...")
		} else {
			// Create TUI program
			model := tui.InitialRootModel(port, Version)
			serverProgram = tea.NewProgram(model, tea.WithAltScreen())

			// Run the TUI (blocking)
			if _, err := serverProgram.Run(); err != nil {
				fmt.Printf("Error: %v\n", err)
				os.Exit(1)
			}
		}

		// Cleanup port file on exit
		removeActivePort()
	},
}

// findAvailablePort tries ports starting from 'start' until one is available
func findAvailablePort(start int) (int, net.Listener) {
	for port := start; port < start+100; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return port, ln
		}
	}
	return 0, nil
}

// saveActivePort writes the active port to ~/.surge/port for extension discovery
func saveActivePort(port int) {
	portFile := filepath.Join(config.GetSurgeDir(), "port")
	os.WriteFile(portFile, []byte(fmt.Sprintf("%d", port)), 0644)
	utils.Debug("HTTP server listening on port %d", port)
}

// removeActivePort cleans up the port file on exit
func removeActivePort() {
	portFile := filepath.Join(config.GetSurgeDir(), "port")
	os.Remove(portFile)
}

// startHTTPServer starts the HTTP server using an existing listener
func startHTTPServer(ln net.Listener, port int, defaultOutputDir string) {
	mux := http.NewServeMux()

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"port":   port,
		})
	})

	// Download endpoint
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		handleDownload(w, r, defaultOutputDir)
	})

	server := &http.Server{Handler: corsMiddleware(mux)}
	if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
		utils.Debug("HTTP server error: %v", err)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

// DownloadRequest represents a download request from the browser extension
type DownloadRequest struct {
	URL      string `json:"url"`
	Filename string `json:"filename,omitempty"`
	Path     string `json:"path,omitempty"`
}

func handleDownload(w http.ResponseWriter, r *http.Request, defaultOutputDir string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req DownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if req.URL == "" {
		http.Error(w, "URL is required", http.StatusBadRequest)
		return
	}

	if strings.Contains(req.Path, "..") || strings.Contains(req.Filename, "..") {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	if strings.Contains(req.Filename, "/") || strings.Contains(req.Filename, "\\") {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}
	if strings.Contains(req.Filename, "/") || strings.Contains(req.Filename, "\\") {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}
	// Absolute paths are allowed for local tool usage
	// if filepath.IsAbs(req.Path) { ... }

	// Don't default to "." here, let TUI handle it
	// if req.Path == "" {
	// 	req.Path = "."
	// }

	utils.Debug("Received download request: URL=%s, Path=%s", req.URL, req.Path)

	// HEADLESS MODE: If serverProgram is nil, we are running without TUI.
	if serverProgram == nil {
		go func() {
			// For headless, default to settings or current directory if not specified
			outPath := req.Path
			if outPath == "" {
				if defaultOutputDir != "" {
					outPath = defaultOutputDir
					_ = os.MkdirAll(outPath, 0755)
				} else {
					settings, err := config.LoadSettings()
					if err == nil && settings.General.DefaultDownloadDir != "" {
						outPath = settings.General.DefaultDownloadDir
						// Create directory if it doesn't exist
						_ = os.MkdirAll(outPath, 0755)
					} else {
						outPath = "."
					}
				}
			}

			// Increment active downloads
			atomic.AddInt32(&activeDownloads, 1)

			fmt.Printf("Starting headless download: %s -> %s\n", req.URL, outPath)
			ctx := context.Background()

			// Struct for stats
			var totalSize int64
			startTime := time.Now()

			// Create a channel effectively to ignore events or log them
			eventCh := make(chan tea.Msg, 10)
			errCh := make(chan error, 1)

			// Run download in background
			go func() {
				defer atomic.AddInt32(&activeDownloads, -1)
				err := download.Download(ctx, req.URL, outPath, false, eventCh, uuid.New().String())
				errCh <- err
				close(eventCh)
			}()

			// Process events (blocking until eventCh is closed)
			for msg := range eventCh {
				switch m := msg.(type) {
				case messages.DownloadStartedMsg:
					totalSize = m.Total
					startTime = time.Now() // Reset start time (after probe)
					fmt.Printf("Downloading: %s (%s)\n", m.Filename, utils.ConvertBytesToHumanReadable(totalSize))
				case messages.DownloadErrorMsg:
					fmt.Printf("Download error for %s: %v\n", req.URL, m.Err)
				}
			}

			// Check final result
			if err := <-errCh; err != nil {
				fmt.Printf("Download failed: %v\n", err)
			} else {
				elapsed := time.Since(startTime)
				speed := float64(totalSize) / elapsed.Seconds() / (1024 * 1024)
				fmt.Printf("Download complete: %s in %s (%.2f MB/s)\n",
					req.URL, elapsed.Round(time.Millisecond), speed)
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "started",
			"message": "Download started in background (headless)",
		})
		return
	}

	// TUI MODE: Send message to TUI to start download
	serverProgram.Send(tui.StartDownloadMsg{
		URL:      req.URL,
		Path:     req.Path,
		Filename: req.Filename,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "queued",
		"message": "Download request received",
	})
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(getCmd)
	rootCmd.Flags().Bool("headless", false, "Run in headless mode (no TUI)")
	rootCmd.Flags().IntP("port", "p", 0, "Port to listen on (default: 8080 or first available)")
	rootCmd.Flags().StringP("output", "o", "", "Default output directory (headless mode only)")
	rootCmd.SetVersionTemplate("Surge version {{.Version}}\n")
}

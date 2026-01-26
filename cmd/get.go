package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/surge-downloader/surge/internal/utils"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

const progressChannelBuffer = 100

// readURLsFromFile reads URLs from a file, one per line
func readURLsFromFile(filepath string) ([]string, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	var urls []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip empty lines and comments
		if line != "" && !strings.HasPrefix(line, "#") {
			urls = append(urls, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	if len(urls) == 0 {
		return nil, fmt.Errorf("no URLs found in file")
	}

	return urls, nil
}

// runHeadless runs a download without TUI, printing progress to stderr
func runHeadless(ctx context.Context, url, outPath string, verbose bool) error {
	eventCh := make(chan tea.Msg, progressChannelBuffer)

	startTime := time.Now()
	var totalSize int64
	var lastProgress int64

	// Start download in background
	errCh := make(chan error, 1)
	go func() {
		err := download.Download(ctx, url, outPath, verbose, eventCh, uuid.New().String())
		errCh <- err
		close(eventCh)
	}()

	// Process events
	for msg := range eventCh {
		switch m := msg.(type) {
		case messages.DownloadStartedMsg:
			// Reset start time to exclude probing time
			startTime = time.Now()
			totalSize = m.Total
			fmt.Fprintf(os.Stderr, "Downloading: %s (%s)\n", m.Filename, utils.ConvertBytesToHumanReadable(totalSize))
		case messages.ProgressMsg:
			if totalSize > 0 {
				percent := m.Downloaded * 100 / totalSize
				lastPercent := lastProgress * 100 / totalSize
				if percent/10 > lastPercent/10 {
					speed := float64(m.Downloaded) / time.Since(startTime).Seconds() / (1024 * 1024)
					fmt.Fprintf(os.Stderr, "  %d%% (%s) - %.2f MB/s\n", percent,
						utils.ConvertBytesToHumanReadable(m.Downloaded), speed)
				}
				lastProgress = m.Downloaded
			}
		case messages.DownloadCompleteMsg:
			elapsed := time.Since(startTime)
			speed := float64(totalSize) / elapsed.Seconds() / (1024 * 1024)
			fmt.Fprintf(os.Stderr, "Complete: %s in %s (%.2f MB/s)\n",
				utils.ConvertBytesToHumanReadable(totalSize),
				elapsed.Round(time.Millisecond), speed)
		case messages.DownloadErrorMsg:
			return m.Err
		}
	}

	err := <-errCh
	if err == nil {
		elapsed := time.Since(startTime)
		speed := float64(totalSize) / elapsed.Seconds() / (1024 * 1024)
		fmt.Fprintf(os.Stderr, "Complete: %s in %s (%.2f MB/s)\n",
			utils.ConvertBytesToHumanReadable(totalSize),
			elapsed.Round(time.Millisecond), speed)
	}
	return err
}

// sendToServer sends a download request to a running surge server
func sendToServer(url, outPath string, port int) error {
	reqBody := DownloadRequest{
		URL:  url,
		Path: outPath,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	serverURL := fmt.Sprintf("http://127.0.0.1:%d/download", port)
	resp, err := http.Post(serverURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server error: %s - %s", resp.Status, string(body))
	}

	fmt.Printf("Download queued: %s\n", string(body))
	return nil
}

var getCmd = &cobra.Command{
	Use:   "get [url]",
	Short: "Download a file in headless mode or send to running server",
	Long: `Download a file from a URL without the TUI interface.

Use --port to send the download to a running Surge instance.
Use --batch to download multiple URLs from a file (one URL per line).`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		outPath, _ := cmd.Flags().GetString("output")
		// verbose, _ := cmd.Flags().GetBool("verbose") // Verbose not used in server mode easily
		portFlag, _ := cmd.Flags().GetInt("port")
		batchFile, _ := cmd.Flags().GetString("batch")

		// Collect URLs to download
		var urls []string
		if batchFile != "" {
			// Batch mode: read URLs from file
			var err error
			urls, err = readURLsFromFile(batchFile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			// ... (duplicate filtering omitted for brevity, logic maintained)
			seen := make(map[string]bool)
			uniqueURLs := make([]string, 0, len(urls))
			for _, url := range urls {
				normalized := strings.TrimRight(url, "/")
				if !seen[normalized] {
					seen[normalized] = true
					uniqueURLs = append(uniqueURLs, url)
				}
			}
			urls = uniqueURLs
		} else if len(args) == 1 {
			urls = []string{args[0]}
		} else {
			fmt.Fprintf(os.Stderr, "Error: requires either a URL argument or --batch flag\n")
			os.Exit(1)
		}

		// Try to acquire lock
		isMaster, err := AcquireLock()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error checking lock: %v\n", err)
			os.Exit(1)
		}

		var targetPort int

		if isMaster {
			defer ReleaseLock()
			// We are the master. Start the server.
			var ln net.Listener
			if portFlag > 0 {
				targetPort = portFlag
				ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", targetPort))
			} else {
				targetPort, ln = findAvailablePort(8080)
			}

			if err != nil || ln == nil {
				fmt.Fprintf(os.Stderr, "Error starting server: %v\n", err)
				os.Exit(1)
			}

			saveActivePort(targetPort)
			defer removeActivePort()

			// Start server in background
			go startHTTPServer(ln, targetPort, outPath)

			fmt.Printf("Surge %s (Headless Host) running on port %d\n", Version, targetPort)
		} else {
			// We are the client. Find the master's port.
			// If user specified --port, use that. Otherwise read from file.
			if portFlag > 0 {
				targetPort = portFlag
			} else {
				// Read port file
				portFile := filepath.Join(config.GetSurgeDir(), "port")
				data, err := os.ReadFile(portFile)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: Surge is running but could not read port file: %v\n", err)
					os.Exit(1)
				}
				fmt.Sscanf(string(data), "%d", &targetPort)
			}
		}

		// Send downloads to targetPort (whether it's us or another instance)
		// If we are master, we send to ourselves via HTTP. This unifies the code pathway.

		var failed int
		for i, url := range urls {
			if len(urls) > 1 {
				fmt.Fprintf(os.Stderr, "\n[%d/%d] %s\n", i+1, len(urls), url)
			}

			// If we are master, we might want to default outPath if not set
			// logic handled in handleDownload or just before sending
			reqPath := outPath
			if reqPath == "" && isMaster {
				// For master, empty path means "use default" in handleDownload
				reqPath = ""
			}

			if err := sendToServer(url, reqPath, targetPort); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				failed++
			}
		}

		if !isMaster {
			// Client mode: Exit after queuing
			if failed > 0 {
				os.Exit(1)
			}
			return
		}

		// Master mode: Wait for downloads to finish
		// We wait until activeDownloads > 0 (to ensure at least started), then wait for it to be 0

		// Give a small moment for the HTTP request to process and increment the counter
		time.Sleep(500 * time.Millisecond)

		// Wait Loop
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		// Handle Ctrl+C
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

		fmt.Println("Waiting for downloads to complete... (Ctrl+C to stop)")

		for {
			select {
			case <-sigChan:
				fmt.Println("\nStopping...")
				return
			case <-ticker.C:
				current := atomic.LoadInt32(&activeDownloads)
				if current == 0 {
					fmt.Println("All downloads complete. Exiting.")
					return
				}
			}
		}
	},
}

func init() {
	getCmd.Flags().StringP("output", "o", "", "output directory")
	getCmd.Flags().BoolP("verbose", "v", false, "verbose output")
	getCmd.Flags().IntP("port", "p", 0, "send to running surge server on this port")
	getCmd.Flags().StringP("batch", "b", "", "file containing URLs to download (one per line)")
}

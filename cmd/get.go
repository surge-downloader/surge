package cmd

import (
	"bufio"
	"bytes"
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
	"github.com/surge-downloader/surge/internal/engine/state"

	"github.com/spf13/cobra"
)

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

	var respData map[string]interface{}
	if err := json.Unmarshal(body, &respData); err != nil {
		// Fallback to plain string if JSON parse fails (shouldn't happen with new server)
		fmt.Printf("Download queued: %s\n", string(body))
		return nil
	}

	if id, ok := respData["id"].(string); ok {
		fmt.Printf("Download queued. ID: %s\n", id)
	} else {
		fmt.Printf("Download queued.\n")
	}

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
		idFlag, _ := cmd.Flags().GetString("id")
		outPath, _ := cmd.Flags().GetString("output")
		// verbose, _ := cmd.Flags().GetBool("verbose")
		portFlag, _ := cmd.Flags().GetInt("port")
		batchFile, _ := cmd.Flags().GetString("batch")

		// 1. Handle Status Query (--id)
		if idFlag != "" {
			handleStatusQuery(idFlag, portFlag)
			return
		}

		// 2. Handle Download Queueing
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
			fmt.Fprintf(os.Stderr, "Error: requires either a URL argument or --batch flag (or --id to query status)\n")
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

			// Start consuming progress messages
			StartHeadlessConsumer()
		} else {
			// We are the client. Find the master's port.
			if portFlag > 0 {
				targetPort = portFlag
			} else {
				// Read port file
				targetPort = readActivePort()
			}
		}

		// Send downloads to targetPort
		var failed int
		for i, url := range urls {
			if len(urls) > 1 {
				fmt.Fprintf(os.Stderr, "\n[%d/%d] %s\n", i+1, len(urls), url)
			}

			reqPath := outPath
			if reqPath == "" && isMaster {
				reqPath = ""
			}

			if err := sendToServer(url, reqPath, targetPort); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				failed++
			}
		}

		if !isMaster {
			if failed > 0 {
				os.Exit(1)
			}
			return
		}

		// Master mode: Wait for downloads to finish
		time.Sleep(500 * time.Millisecond)

		// Wait Loop
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

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
	getCmd.Flags().String("id", "", "get status of a specific download by ID")
}

func readActivePort() int {
	portFile := filepath.Join(config.GetSurgeDir(), "port")
	data, err := os.ReadFile(portFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Surge is running but could not read port file: %v\n", err)
		os.Exit(1)
	}
	var port int
	fmt.Sscanf(string(data), "%d", &port)
	return port
}

func handleStatusQuery(id string, portFlag int) {
	// Need to find running instance port
	port := portFlag
	surgeRunning := false

	if port == 0 {
		isMaster, err := AcquireLock()
		if err == nil && isMaster {
			ReleaseLock()
			surgeRunning = false
		} else {
			surgeRunning = true
			port = readActivePort()
		}
	} else {
		surgeRunning = true
	}

	foundViaHTTP := false
	if surgeRunning {
		url := fmt.Sprintf("http://127.0.0.1:%d/download?id=%s", port, id)
		resp, err := http.Get(url)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var status map[string]interface{}
				if err := json.NewDecoder(resp.Body).Decode(&status); err == nil {
					printStatusTable(status)
					foundViaHTTP = true
					return
				}
			}
		}
	}

	if !foundViaHTTP {
		// Fallback to SQLite
		entry, err := state.GetDownload(id)
		if err != nil {
			// If not found in DB either, then it really doesn't exist
			// But GetDownload returns nil, nil for not found (based on my implementation)
			// Wait, I implemented it to return nil, nil if sql.ErrNoRows.
			// Let's check error handling in GetDownload.
			// Yes: if err == sql.ErrNoRows { return nil, nil }
		}

		if entry == nil {
			fmt.Printf("Download ID %s not found.\n", id)
			if !surgeRunning {
				fmt.Println("(Surge is not running)")
			}
			os.Exit(1)
		}

		// Convert to map for printStatusTable
		// We calculate progress based on TotalSize and Downloaded
		var progress float64
		if entry.TotalSize > 0 {
			progress = float64(entry.Downloaded) / float64(entry.TotalSize) * 100
		} else if entry.Status == "completed" {
			progress = 100.0
		}

		// Calculate speed... meaningless for static entry unless we store it?
		// We store TimeTaken for completed.
		var speed float64
		if entry.Status == "completed" && entry.TimeTaken > 0 {
			// TimeTaken is in ms. TotalSize is bytes.
			// Speed in MB/s = (TotalSize / 1024 / 1024) / (TimeTaken / 1000)
			// = TotalSize * 1000 / TimeTaken / 1024 / 1024
			speed = float64(entry.TotalSize) * 1000 / float64(entry.TimeTaken) / 1024 / 1024
		}

		status := map[string]interface{}{
			"id":       entry.ID,
			"filename": entry.Filename,
			"status":   entry.Status,
			"progress": progress,
			"speed":    speed,
		}

		printStatusTable(status)
	}
}

func printStatusTable(s map[string]interface{}) {
	fmt.Printf("ID:        %v\n", s["id"])
	fmt.Printf("File:      %v\n", s["filename"])
	fmt.Printf("Status:    %v\n", s["status"])
	fmt.Printf("Progress:  %.1f%%\n", s["progress"])
	fmt.Printf("Speed:     %.2f MB/s\n", s["speed"])
	if err, ok := s["error"]; ok && err != "" {
		fmt.Printf("Error:     %v\n", err)
	}
}

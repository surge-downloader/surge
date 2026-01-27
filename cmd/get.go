package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/surge-downloader/surge/internal/config"
	"github.com/surge-downloader/surge/internal/engine/state"
	"github.com/surge-downloader/surge/internal/engine/types"

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
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("server error: %s - %s", resp.Status, string(body))
	}

	var respData map[string]interface{}
	if err := json.Unmarshal(body, &respData); err != nil {
		// Fallback to plain string if JSON parse fails (shouldn't happen with new server)
		fmt.Printf("Download queued: %s\n", string(body))
		return nil
	}

	if resp.StatusCode == http.StatusAccepted {
		fmt.Printf("Duplicate/Extension download. Waiting for approval from TUI\n")
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
	Args: cobra.RangeArgs(1, 2),
	Run: func(cmd *cobra.Command, args []string) {
		initializeGlobalState()

		outPath, _ := cmd.Flags().GetString("output")
		portFlag, _ := cmd.Flags().GetInt("port")
		batchFile, _ := cmd.Flags().GetString("batch")

		// 1. Handle Batch Mode (flags only)
		if batchFile != "" {
			var urls []string
			var err error
			urls, err = readURLsFromFile(batchFile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			processDownloads(urls, outPath, portFlag)
			return
		}

		// 2. Handle Positional Arguments
		if len(args) == 2 {
			id := args[0]
			action := strings.ToLower(args[1])

			switch action {
			case "info":
				handleStatusQuery(id, portFlag)
			case "pause":
				handleAction(id, "pause", portFlag)
			case "resume":
				handleAction(id, "resume", portFlag)
			case "delete":
				handleAction(id, "delete", portFlag)
			default:
				fmt.Fprintf(os.Stderr, "Error: invalid action '%s'. Valid actions are: info, pause, resume, delete\n", action)
				os.Exit(1)
			}
			return
		}

		// 3. Handle Single URL Download
		url := args[0]
		processDownloads([]string{url}, outPath, portFlag)
	},
}

func init() {
	getCmd.Flags().StringP("output", "o", "", "output directory")
	getCmd.Flags().BoolP("verbose", "v", false, "verbose output")
	getCmd.Flags().IntP("port", "p", 0, "send to running surge server on this port")
	getCmd.Flags().StringP("batch", "b", "", "file containing URLs to download (one per line)")
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

func handleAction(id string, action string, portFlag int) {
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

	if surgeRunning {
		url := fmt.Sprintf("http://127.0.0.1:%d/%s?id=%s", port, action, id)
		method := http.MethodPost
		if action == "delete" {
			// server handler also accepts POST, but let's try to be proper or just use POST as implemented in root
			method = http.MethodPost
		}

		req, err := http.NewRequest(method, url, nil)
		if err != nil {
			fmt.Printf("Failed to create request: %v\n", err)
			os.Exit(1)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Printf("Failed to connect to server: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			fmt.Printf("Download %s action successful for ID: %s\n", action, id)
		} else {
			body, _ := io.ReadAll(resp.Body)
			fmt.Printf("Failed to %s download: %s - %s\n", action, resp.Status, string(body))
			os.Exit(1)
		}
		return
	}

	// Offline handling
	if action == "delete" {
		if err := state.RemoveFromMasterList(id); err != nil {
			fmt.Printf("Error deleting download: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Download deleted (offline).\n")
		return
	}

	// For pause/resume, we need to fetch, update, and save
	entry, err := state.GetDownload(id)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	if entry == nil {
		fmt.Printf("Download ID %s not found.\n", id)
		os.Exit(1)
	}

	if action == "pause" {
		entry.Status = "paused"
	} else if action == "resume" {
		entry.Status = "queued"
	}

	if err := state.AddToMasterList(*entry); err != nil {
		fmt.Printf("Error updating state: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Download %sd (offline).\n", action)
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
				var status types.DownloadStatus
				if err := json.NewDecoder(resp.Body).Decode(&status); err == nil {
					printStatusTable(&status)
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
			fmt.Fprintf(os.Stderr, "Error querying database: %v\n", err)
			os.Exit(1)
		}

		if entry == nil {
			fmt.Printf("Download ID %s not found.\n", id)
			if !surgeRunning {
				fmt.Println("(Surge is not running)")
			}
			os.Exit(1)
		}

		// Convert to unified DownloadStatus for printing
		var progress float64
		if entry.TotalSize > 0 {
			progress = float64(entry.Downloaded) * 100 / float64(entry.TotalSize)
		} else if entry.Status == "completed" {
			progress = 100.0
		}

		var speed float64
		if entry.Status == "completed" && entry.TimeTaken > 0 {
			speed = float64(entry.TotalSize) * 1000 / float64(entry.TimeTaken) / (1024 * 1024)
		}

		status := &types.DownloadStatus{
			ID:         entry.ID,
			Filename:   entry.Filename,
			Status:     entry.Status,
			Progress:   progress,
			Speed:      speed,
			Downloaded: entry.Downloaded,
			TotalSize:  entry.TotalSize,
		}

		printStatusTable(status)
	}
}

func printStatusTable(s *types.DownloadStatus) {
	fmt.Printf("ID:        %v\n", s.ID)
	fmt.Printf("File:      %v\n", s.Filename)
	fmt.Printf("Status:    %v\n", s.Status)
	fmt.Printf("Progress:  %.1f%%\n", s.Progress)
	fmt.Printf("Speed:     %.2f MB/s\n", s.Speed)
	if s.Error != "" {
		fmt.Printf("Error:     %v\n", s.Error)
	}
}

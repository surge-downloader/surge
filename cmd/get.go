package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
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
		verbose, _ := cmd.Flags().GetBool("verbose")
		port, _ := cmd.Flags().GetInt("port")
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

			// Filter out duplicate URLs
			seen := make(map[string]bool)
			uniqueURLs := make([]string, 0, len(urls))
			for _, url := range urls {
				normalized := strings.TrimRight(url, "/")
				if !seen[normalized] {
					seen[normalized] = true
					uniqueURLs = append(uniqueURLs, url)
				}
			}
			duplicates := len(urls) - len(uniqueURLs)
			urls = uniqueURLs

			if duplicates > 0 {
				fmt.Fprintf(os.Stderr, "Loaded %d URLs from %s (%d duplicates ignored)\n", len(urls), batchFile, duplicates)
			} else {
				fmt.Fprintf(os.Stderr, "Loaded %d URLs from %s\n", len(urls), batchFile)
			}
		} else if len(args) == 1 {
			// Single URL mode
			urls = []string{args[0]}
		} else {
			fmt.Fprintf(os.Stderr, "Error: requires either a URL argument or --batch flag\n")
			os.Exit(1)
		}

		if outPath == "" && port == 0 {
			// Try to load default download directory from settings
			settings, err := config.LoadSettings()
			if err == nil && settings.General.DefaultDownloadDir != "" {
				outPath = settings.General.DefaultDownloadDir
				// Create directory if it doesn't exist
				if err := os.MkdirAll(outPath, 0755); err != nil {
					// If creation fails, fallback to current directory
					outPath = "."
				}
			} else {
				// Fallback to current directory
				outPath = "."
			}
		} else if outPath == "" && port > 0 {
			// For server mode, send empty path so server/TUI uses its default
			outPath = ""
		}

		// Process each URL
		var failed int
		for i, url := range urls {
			if len(urls) > 1 {
				fmt.Fprintf(os.Stderr, "\n[%d/%d] %s\n", i+1, len(urls), url)
			}

			if port > 0 {
				// Send to running server
				if err := sendToServer(url, outPath, port); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					failed++
				}
			} else {
				// Headless download
				ctx := context.Background()
				if err := runHeadless(ctx, url, outPath, verbose); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					failed++
				}
			}
		}

		if failed > 0 {
			fmt.Fprintf(os.Stderr, "\n%d of %d downloads failed\n", failed, len(urls))
			os.Exit(1)
		}
	},
}

func init() {
	getCmd.Flags().StringP("output", "o", "", "output directory")
	getCmd.Flags().BoolP("verbose", "v", false, "verbose output")
	getCmd.Flags().IntP("port", "p", 0, "send to running surge server on this port")
	getCmd.Flags().StringP("batch", "b", "", "file containing URLs to download (one per line)")
}

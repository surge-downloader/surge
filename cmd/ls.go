package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/surge-downloader/surge/internal/engine/state"
	"github.com/surge-downloader/surge/internal/engine/types"
)

var lsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List downloads",
	Long:  `List all downloads from the running server or database.`,
	Run: func(cmd *cobra.Command, args []string) {
		initializeGlobalState()

		jsonOutput, _ := cmd.Flags().GetBool("json")
		watch, _ := cmd.Flags().GetBool("watch")

		if watch {
			for {
				// Clear screen first for watch mode
				fmt.Print("\033[H\033[2J")
				printDownloads(jsonOutput)
				time.Sleep(1 * time.Second)
			}
		} else {
			printDownloads(jsonOutput)
		}
	},
}

// downloadInfo is a unified structure for display
type downloadInfo struct {
	ID         string  `json:"id"`
	Filename   string  `json:"filename"`
	Status     string  `json:"status"`
	Progress   float64 `json:"progress"`
	TotalSize  int64   `json:"total_size"`
	Downloaded int64   `json:"downloaded"`
	Speed      float64 `json:"speed,omitempty"`
}

func printDownloads(jsonOutput bool) {
	var downloads []downloadInfo

	// Try to get from running server first
	port := readActivePort()
	if port > 0 {
		serverDownloads := getDownloadsFromServer(port)
		if serverDownloads != nil {
			downloads = serverDownloads
		}
	}

	// If no server running or no active downloads, fall back to database
	if len(downloads) == 0 {
		dbDownloads, err := state.ListAllDownloads()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing downloads: %v\n", err)
			os.Exit(1)
		}

		for _, d := range dbDownloads {
			var progress float64
			if d.TotalSize > 0 {
				progress = float64(d.Downloaded) * 100 / float64(d.TotalSize)
			}
			downloads = append(downloads, downloadInfo{
				ID:         d.ID,
				Filename:   d.Filename,
				Status:     d.Status,
				Progress:   progress,
				TotalSize:  d.TotalSize,
				Downloaded: d.Downloaded,
			})
		}
	}

	if len(downloads) == 0 {
		if !jsonOutput {
			fmt.Println("No downloads found.")
		} else {
			fmt.Println("[]")
		}
		return
	}

	if jsonOutput {
		data, _ := json.MarshalIndent(downloads, "", "  ")
		fmt.Println(string(data))
		return
	}

	// Table output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tFILENAME\tSTATUS\tPROGRESS\tSPEED\tSIZE")
	fmt.Fprintln(w, "--\t--------\t------\t--------\t-----\t----")

	for _, d := range downloads {
		progress := fmt.Sprintf("%.1f%%", d.Progress)
		size := formatSize(d.TotalSize)

		// Speed display
		var speed string
		if d.Speed > 0 {
			speed = fmt.Sprintf("%.1f MB/s", d.Speed)
		} else {
			speed = "-"
		}

		// Truncate ID for display
		id := d.ID
		if len(id) > 8 {
			id = id[:8]
		}

		// Truncate filename
		filename := d.Filename
		if len(filename) > 25 {
			filename = filename[:22] + "..."
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", id, filename, d.Status, progress, speed, size)
	}
	w.Flush()
}

func getDownloadsFromServer(port int) []downloadInfo {
	// Query the server's /list endpoint (we need to add this endpoint)
	// For now, we'll return nil and the database fallback will be used
	// TODO: Add /list endpoint to server

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/list", port))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	var statuses []types.DownloadStatus
	if err := json.Unmarshal(body, &statuses); err != nil {
		return nil
	}

	var downloads []downloadInfo
	for _, s := range statuses {
		downloads = append(downloads, downloadInfo{
			ID:         s.ID,
			Filename:   s.Filename,
			Status:     s.Status,
			Progress:   s.Progress,
			TotalSize:  s.TotalSize,
			Downloaded: s.Downloaded,
			Speed:      s.Speed,
		})
	}

	return downloads
}

func formatSize(bytes int64) string {
	if bytes == 0 {
		return "-"
	}
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func init() {
	rootCmd.AddCommand(lsCmd)
	lsCmd.Flags().Bool("json", false, "Output in JSON format")
	lsCmd.Flags().Bool("watch", false, "Watch mode: refresh every second")
}

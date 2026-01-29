package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/surge-downloader/surge/internal/engine/state"
)

var lsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List downloads",
	Long:  `List all downloads from the database.`,
	Run: func(cmd *cobra.Command, args []string) {
		initializeGlobalState()

		jsonOutput, _ := cmd.Flags().GetBool("json")
		watch, _ := cmd.Flags().GetBool("watch")

		if watch {
			for {
				printDownloads(jsonOutput)
				time.Sleep(2 * time.Second)
				// Clear screen for watch mode
				fmt.Print("\033[H\033[2J")
			}
		} else {
			printDownloads(jsonOutput)
		}
	},
}

func printDownloads(jsonOutput bool) {
	downloads, err := state.ListAllDownloads()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing downloads: %v\n", err)
		os.Exit(1)
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
	fmt.Fprintln(w, "ID\tFILENAME\tSTATUS\tPROGRESS\tSIZE")
	fmt.Fprintln(w, "--\t--------\t------\t--------\t----")

	for _, d := range downloads {
		var progress string
		if d.TotalSize > 0 {
			pct := float64(d.Downloaded) * 100 / float64(d.TotalSize)
			progress = fmt.Sprintf("%.1f%%", pct)
		} else {
			progress = "-"
		}

		size := formatSize(d.TotalSize)

		// Truncate ID for display
		id := d.ID
		if len(id) > 8 {
			id = id[:8]
		}

		// Truncate filename
		filename := d.Filename
		if len(filename) > 30 {
			filename = filename[:27] + "..."
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", id, filename, d.Status, progress, size)
	}
	w.Flush()
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
	lsCmd.Flags().Bool("watch", false, "Watch mode: refresh every 2 seconds")
}

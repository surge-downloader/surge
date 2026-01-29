package cmd

import (
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"github.com/surge-downloader/surge/internal/engine/state"
)

var rmCmd = &cobra.Command{
	Use:     "rm <ID>",
	Aliases: []string{"kill"},
	Short:   "Remove a download",
	Long:    `Remove a download by its ID. Use --clean to remove all completed downloads.`,
	Args:    cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		initializeGlobalState()

		clean, _ := cmd.Flags().GetBool("clean")

		if !clean && len(args) == 0 {
			fmt.Fprintln(os.Stderr, "Error: provide a download ID or use --clean")
			os.Exit(1)
		}

		port := readActivePort()

		if clean {
			// Remove completed downloads from DB
			count, err := state.RemoveCompletedDownloads()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error cleaning downloads: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Removed %d completed downloads.\n", count)
			return
		}

		id := args[0]

		// Resolve partial ID to full ID
		id, err := resolveDownloadID(id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if port > 0 {
			// Send to running server
			resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/delete?id=%s", port, id), "application/json", nil)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error connecting to server: %v\n", err)
				os.Exit(1)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				fmt.Fprintf(os.Stderr, "Error: server returned %s\n", resp.Status)
				os.Exit(1)
			}
			fmt.Printf("Removed download %s\n", id[:8])
		} else {
			// Offline mode: remove from DB
			if err := state.RemoveFromMasterList(id); err != nil {
				fmt.Fprintf(os.Stderr, "Error removing download: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Removed download %s (offline mode)\n", id[:8])
		}
	},
}

func init() {
	rootCmd.AddCommand(rmCmd)
	rmCmd.Flags().Bool("clean", false, "Remove all completed downloads")
}

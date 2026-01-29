package cmd

import (
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"github.com/surge-downloader/surge/internal/engine/state"
)

var pauseCmd = &cobra.Command{
	Use:   "pause <ID>",
	Short: "Pause a download",
	Long:  `Pause a download by its ID. Use --all to pause all downloads.`,
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		initializeGlobalState()

		all, _ := cmd.Flags().GetBool("all")

		if !all && len(args) == 0 {
			fmt.Fprintln(os.Stderr, "Error: provide a download ID or use --all")
			os.Exit(1)
		}

		port := readActivePort()

		if all {
			// Pause all downloads
			if port > 0 {
				// TODO: Implement /pause-all endpoint or iterate
				fmt.Println("Pausing all downloads is not yet implemented for running server.")
			} else {
				// Offline mode: update DB directly
				if err := state.PauseAllDownloads(); err != nil {
					fmt.Fprintf(os.Stderr, "Error pausing downloads: %v\n", err)
					os.Exit(1)
				}
				fmt.Println("All downloads paused.")
			}
			return
		}

		id := args[0]

		if port > 0 {
			// Send to running server
			resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/pause?id=%s", port, id), "application/json", nil)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error connecting to server: %v\n", err)
				os.Exit(1)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				fmt.Fprintf(os.Stderr, "Error: server returned %s\n", resp.Status)
				os.Exit(1)
			}
			fmt.Printf("Paused download %s\n", id)
		} else {
			// Offline mode: update DB directly
			if err := state.UpdateStatus(id, "paused"); err != nil {
				fmt.Fprintf(os.Stderr, "Error pausing download: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Paused download %s (offline mode)\n", id)
		}
	},
}

func init() {
	rootCmd.AddCommand(pauseCmd)
	pauseCmd.Flags().Bool("all", false, "Pause all downloads")
}

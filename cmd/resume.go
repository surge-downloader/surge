package cmd

import (
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"github.com/surge-downloader/surge/internal/engine/state"
)

var resumeCmd = &cobra.Command{
	Use:   "resume <ID>",
	Short: "Resume a paused download",
	Long:  `Resume a paused download by its ID. Use --all to resume all paused downloads.`,
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
			if port > 0 {
				fmt.Println("Resuming all downloads is not yet implemented for running server.")
			} else {
				if err := state.ResumeAllDownloads(); err != nil {
					fmt.Fprintf(os.Stderr, "Error resuming downloads: %v\n", err)
					os.Exit(1)
				}
				fmt.Println("All downloads resumed. Start Surge to begin downloading.")
			}
			return
		}

		id := args[0]

		if port > 0 {
			// Send to running server
			resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/resume?id=%s", port, id), "application/json", nil)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error connecting to server: %v\n", err)
				os.Exit(1)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				fmt.Fprintf(os.Stderr, "Error: server returned %s\n", resp.Status)
				os.Exit(1)
			}
			fmt.Printf("Resumed download %s\n", id)
		} else {
			if err := state.UpdateStatus(id, "queued"); err != nil {
				fmt.Fprintf(os.Stderr, "Error resuming download: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Resumed download %s (offline mode). Start Surge to begin downloading.\n", id)
		}
	},
}

func init() {
	rootCmd.AddCommand(resumeCmd)
	resumeCmd.Flags().Bool("all", false, "Resume all paused downloads")
}

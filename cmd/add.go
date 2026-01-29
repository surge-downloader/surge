package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var addCmd = &cobra.Command{
	Use:     "add [url]...",
	Aliases: []string{"get"},
	Short:   "Add a new download to the running Surge instance",
	Long:    `Add one or more URLs to the download queue of a running Surge instance.`,
	Run: func(cmd *cobra.Command, args []string) {
		// Initialize Global State (needed for config/paths)
		initializeGlobalState()

		batchFile, _ := cmd.Flags().GetString("batch")
		output, _ := cmd.Flags().GetString("output")

		// Collect URLs
		var urls []string

		// 1. URLs from args
		urls = append(urls, args...)

		// 2. URLs from batch file
		if batchFile != "" {
			fileUrls, err := readURLsFromFile(batchFile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading batch file: %v\n", err)
				os.Exit(1)
			}
			urls = append(urls, fileUrls...)
		}

		if len(urls) == 0 {
			cmd.Help()
			return
		}

		// Check if Surge is running
		port := readActivePort()
		if port == 0 {
			fmt.Println("Error: Surge is not running.")
			fmt.Println("Use 'surge <url>' to start Surge with a download.")
			os.Exit(1)
		}

		// Send downloads to server
		count := 0
		for _, url := range urls {
			err := sendToServer(url, output, port)
			if err != nil {
				fmt.Printf("Error adding %s: %v\n", url, err)
			} else {
				count++
			}
		}

		if count > 0 {
			fmt.Printf("Successfully added %d downloads.\n", count)
		}
	},
}

func init() {
	rootCmd.AddCommand(addCmd)
	addCmd.Flags().StringP("batch", "b", "", "File containing URLs to download (one per line)")
	addCmd.Flags().StringP("output", "o", "", "Output directory")
}

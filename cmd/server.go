package cmd

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/surge-downloader/surge/internal/config"
	"github.com/surge-downloader/surge/internal/download"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Manage the Surge background server (daemon)",
	Long:  `Start, stop, or check the status of the Surge background server.`,
}

var serverStartCmd = &cobra.Command{
	Use:   "start [url]...",
	Short: "Start the Surge server in headless mode",
	Run: func(cmd *cobra.Command, args []string) {
		initializeGlobalState()

		// Attempt to acquire lock
		isMaster, err := AcquireLock()
		if err != nil {
			fmt.Printf("Error acquiring lock: %v\n", err)
			os.Exit(1)
		}

		if !isMaster {
			fmt.Fprintln(os.Stderr, "Error: Surge server is already running.")
			os.Exit(1)
		}
		defer ReleaseLock()

		portFlag, _ := cmd.Flags().GetInt("port")
		batchFile, _ := cmd.Flags().GetString("batch")
		outputDir, _ := cmd.Flags().GetString("output")
		exitWhenDone, _ := cmd.Flags().GetBool("exit-when-done")

		// Save current PID to file
		savePID()
		defer removePID()

		// Initialize Global Progress Channel
		GlobalProgressCh = make(chan any, 100)

		// Initialize Global Worker Pool
		GlobalPool = download.NewWorkerPool(GlobalProgressCh, 4)

		// Determine Port
		// Determine Port
		// Logic moved to startServerLogic, or we need to pass flags.
		// Use startServerLogic
		startServerLogic(cmd, args, portFlag, batchFile, outputDir, exitWhenDone)
	},
}

var serverStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the running Surge server",
	Run: func(cmd *cobra.Command, args []string) {
		pid := readPID()
		if pid == 0 {
			fmt.Println("No running Surge server found (PID file missing).")
			return
		}

		process, err := os.FindProcess(pid)
		if err != nil {
			fmt.Printf("Error finding process: %v\n", err)
			return
		}

		// Try to send SIGTERM
		err = process.Signal(syscall.SIGTERM)
		if err != nil {
			fmt.Printf("Error stopping server: %v\n", err)
			return
		}

		fmt.Printf("Sent stop signal to process %d\n", pid)
	},
}

var serverStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check the status of the Surge server",
	Run: func(cmd *cobra.Command, args []string) {
		pid := readPID()
		if pid == 0 {
			fmt.Println("Surge server is NOT running.")
			return
		}

		// Check if process exists
		process, err := os.FindProcess(pid)
		if err != nil {
			fmt.Printf("Surge server is NOT running (Process %d not found).\n", pid)
			// Cleanup stale pid file?
			return
		}

		// Sending signal 0 to check existence
		err = process.Signal(syscall.Signal(0))
		if err != nil {
			fmt.Printf("Surge server is NOT running (Process %d dead).\n", pid)
			return
		}

		port := readActivePort()
		fmt.Printf("Surge server is running (PID: %d, Port: %d).\n", pid, port)
	},
}

func init() {
	rootCmd.AddCommand(serverCmd)
	serverCmd.AddCommand(serverStartCmd)
	serverCmd.AddCommand(serverStopCmd)
	serverCmd.AddCommand(serverStatusCmd)

	serverStartCmd.Flags().StringP("batch", "b", "", "File containing URLs to download")
	serverStartCmd.Flags().IntP("port", "p", 0, "Port to listen on")
	serverStartCmd.Flags().StringP("output", "o", "", "Default output directory")
	serverStartCmd.Flags().Bool("exit-when-done", false, "Exit when all downloads complete")
}

func savePID() {
	pid := os.Getpid()
	pidFile := filepath.Join(config.GetSurgeDir(), "pid")
	os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", pid)), 0644)
}

func removePID() {
	pidFile := filepath.Join(config.GetSurgeDir(), "pid")
	os.Remove(pidFile)
}

func readPID() int {
	pidFile := filepath.Join(config.GetSurgeDir(), "pid")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(string(data))
	return pid
}

func startServerLogic(cmd *cobra.Command, args []string, portFlag int, batchFile string, outputDir string, exitWhenDone bool) {
	var port int
	var listener net.Listener

	if portFlag > 0 {
		port = portFlag
		var err error
		listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: could not bind to port %d: %v\n", port, err)
			os.Exit(1)
		}
	} else {
		port, listener = findAvailablePort(8080)
		if listener == nil {
			fmt.Fprintf(os.Stderr, "Error: could not find available port\n")
			os.Exit(1)
		}
	}

	saveActivePort(port)
	defer removeActivePort()

	go startHTTPServer(listener, port, outputDir)

	// Queue initial downloads
	go func() {
		var urls []string
		urls = append(urls, args...)

		if batchFile != "" {
			fileUrls, err := readURLsFromFile(batchFile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading batch file: %v\n", err)
			} else {
				urls = append(urls, fileUrls...)
			}
		}

		if len(urls) > 0 {
			processDownloads(urls, outputDir, 0)
		}
	}()

	fmt.Printf("Surge %s running in server mode.\n", Version)
	fmt.Printf("HTTP server listening on port %d\n", port)
	fmt.Println("Press Ctrl+C to exit.")

	StartHeadlessConsumer()
	resumePausedDownloads()

	if exitWhenDone {
		go func() {
			time.Sleep(2 * time.Second)
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				if atomic.LoadInt32(&activeDownloads) == 0 {
					if GlobalPool != nil && GlobalPool.ActiveCount() == 0 {
						fmt.Println("All downloads finished. Exiting...")
						// Clean up PID before force exit is nice, but defer won't run on os.Exit
						// Manual cleanup
						removePID()
						removeActivePort()
						os.Exit(0)
					}
				}
			}
		}()
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\nShutting down...")
	if GlobalPool != nil {
		GlobalPool.GracefulShutdown()
	}
}

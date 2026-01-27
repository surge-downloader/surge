package cmd

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

func processDownloads(urls []string, outPath string, portFlag int) {
	// Deduplicate URLs
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

	if len(urls) == 0 {
		return
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
}

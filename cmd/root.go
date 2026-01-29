package cmd

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/surge-downloader/surge/internal/config"
	"github.com/surge-downloader/surge/internal/download"
	"github.com/surge-downloader/surge/internal/engine/events"
	"github.com/surge-downloader/surge/internal/engine/state"
	"github.com/surge-downloader/surge/internal/engine/types"
	"github.com/surge-downloader/surge/internal/tui"
	"github.com/surge-downloader/surge/internal/utils"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// Version information - set via ldflags during build
var (
	Version   = "dev"
	BuildTime = "unknown"
)

// activeDownloads tracks the number of currently running downloads in headless mode
var activeDownloads int32

// Globals for Unified Backend
var (
	GlobalPool       *download.WorkerPool
	GlobalProgressCh chan any
	serverProgram    *tea.Program
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:     "surge [url]...",
	Short:   "An open-source download manager written in Go",
	Long:    `Surge is a blazing fast, open-source terminal (TUI) download manager built in Go.`,
	Version: Version,
	Args:    cobra.ArbitraryArgs,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// Initialize Global Progress Channel
		GlobalProgressCh = make(chan any, 100)

		// Initialize Global Worker Pool
		// TODO: Load max downloads from settings
		GlobalPool = download.NewWorkerPool(GlobalProgressCh, 4)
	},
	Run: func(cmd *cobra.Command, args []string) {

		initializeGlobalState()

		// Attempt to acquire lock
		isMaster, err := AcquireLock()
		if err != nil {
			fmt.Printf("Error acquiring lock: %v\n", err)
			os.Exit(1)
		}

		if !isMaster {
			fmt.Fprintln(os.Stderr, "Error: Surge is already running.")
			fmt.Fprintln(os.Stderr, "Use 'surge add <url>' to add a download to the active instance.")
			os.Exit(1)
		}
		defer ReleaseLock()

		portFlag, _ := cmd.Flags().GetInt("port")
		batchFile, _ := cmd.Flags().GetString("batch")
		outputDir, _ := cmd.Flags().GetString("output")
		noResume, _ := cmd.Flags().GetBool("no-resume")
		exitWhenDone, _ := cmd.Flags().GetBool("exit-when-done")

		var port int
		var listener net.Listener

		if portFlag > 0 {
			// Strict port mode
			port = portFlag
			var err error
			listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: could not bind to port %d: %v\n", port, err)
				os.Exit(1)
			}
		} else {
			// Auto-discovery mode
			port, listener = findAvailablePort(8080)
			if listener == nil {
				fmt.Fprintf(os.Stderr, "Error: could not find available port\n")
				os.Exit(1)
			}
		}

		// Save port for browser extension AND CLI discovery
		saveActivePort(port)
		defer removeActivePort()

		// Start HTTP server in background (reuse the listener)
		go startHTTPServer(listener, port, outputDir)

		// Queue initial downloads if any
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
				processDownloads(urls, outputDir, 0) // 0 port = internal direct add
			}
		}()

		// Auto-resume paused downloads (unless --no-resume)
		if !noResume {
			resumePausedDownloads()
		}

		// Start TUI (default mode)
		startTUI(port, exitWhenDone)
	},
}

// startTUI initializes and runs the TUI program
func startTUI(port int, exitWhenDone bool) {
	// Initialize TUI
	// GlobalPool and GlobalProgressCh are already initialized in PersistentPreRun or Run

	m := tui.InitialRootModel(port, Version, GlobalPool, GlobalProgressCh)
	// m := tui.InitialRootModel(port, Version)
	// No need to instantiate separate pool

	p := tea.NewProgram(m, tea.WithAltScreen())
	serverProgram = p // Save reference for HTTP handler

	// Background listener for progress events
	go func() {
		for msg := range GlobalProgressCh {
			p.Send(msg)
		}
	}()

	// Exit-when-done checker for TUI
	if exitWhenDone {
		go func() {
			// Wait a bit for initial downloads to be queued
			time.Sleep(3 * time.Second)
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				if GlobalPool != nil && GlobalPool.ActiveCount() == 0 {
					// Send quit message to TUI
					p.Send(tea.Quit())
					return
				}
			}
		}()
	}

	// Run TUI
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running program: %v\n", err)
		os.Exit(1)
	}
}

// StartHeadlessConsumer starts a goroutine to consume progress messages and log to stdout
func StartHeadlessConsumer() {
	go func() {
		for msg := range GlobalProgressCh {
			switch m := msg.(type) {
			case events.DownloadStartedMsg:
				id := m.DownloadID
				if len(id) > 8 {
					id = id[:8]
				}
				fmt.Printf("Started: %s [%s]\n", m.Filename, id)
			case events.DownloadCompleteMsg:
				atomic.AddInt32(&activeDownloads, -1)
				id := m.DownloadID
				if len(id) > 8 {
					id = id[:8]
				}
				fmt.Printf("Completed: %s [%s] (in %s)\n", m.Filename, id, m.Elapsed)
			case events.DownloadErrorMsg:
				atomic.AddInt32(&activeDownloads, -1)
				id := m.DownloadID
				if len(id) > 8 {
					id = id[:8]
				}
				fmt.Printf("Error [%s]: %s\n", id, m.Err)
			}
		}
	}()
}

// findAvailablePort tries ports starting from 'start' until one is available
func findAvailablePort(start int) (int, net.Listener) {
	for port := start; port < start+100; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return port, ln
		}
	}
	return 0, nil
}

// saveActivePort writes the active port to ~/.surge/port for extension discovery
func saveActivePort(port int) {
	portFile := filepath.Join(config.GetSurgeDir(), "port")
	os.WriteFile(portFile, []byte(fmt.Sprintf("%d", port)), 0644)
	utils.Debug("HTTP server listening on port %d", port)
}

// removeActivePort cleans up the port file on exit
func removeActivePort() {
	portFile := filepath.Join(config.GetSurgeDir(), "port")
	os.Remove(portFile)
}

// startHTTPServer starts the HTTP server using an existing listener
func startHTTPServer(ln net.Listener, port int, defaultOutputDir string) {
	mux := http.NewServeMux()

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"port":   port,
		})
	})

	// Download endpoint
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		handleDownload(w, r, defaultOutputDir)
	})

	// Pause endpoint
	mux.HandleFunc("/pause", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "Missing id parameter", http.StatusBadRequest)
			return
		}
		if GlobalPool != nil {
			GlobalPool.Pause(id)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "paused", "id": id})
		} else {
			http.Error(w, "Server internal error: pool not initialized", http.StatusInternalServerError)
		}
	})

	// Resume endpoint
	mux.HandleFunc("/resume", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "Missing id parameter", http.StatusBadRequest)
			return
		}
		if GlobalPool != nil {
			GlobalPool.Resume(id)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "resumed", "id": id})
		} else {
			http.Error(w, "Server internal error: pool not initialized", http.StatusInternalServerError)
		}
	})

	// Delete endpoint
	mux.HandleFunc("/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete && r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "Missing id parameter", http.StatusBadRequest)
			return
		}
		if GlobalPool != nil {
			GlobalPool.Cancel(id)
			// Ensure removed from DB as well
			if err := state.RemoveFromMasterList(id); err != nil {
				utils.Debug("Failed to remove from DB: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "id": id})
		} else {
			http.Error(w, "Server internal error: pool not initialized", http.StatusInternalServerError)
		}
	})

	// List endpoint - returns all downloads with current status
	mux.HandleFunc("/list", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var statuses []types.DownloadStatus

		// Get active downloads from pool
		if GlobalPool != nil {
			activeConfigs := GlobalPool.GetAll()
			for _, cfg := range activeConfigs {
				status := types.DownloadStatus{
					ID:       cfg.ID,
					URL:      cfg.URL,
					Filename: cfg.Filename,
					Status:   "downloading",
				}

				if cfg.State != nil {
					status.TotalSize = cfg.State.TotalSize
					status.Downloaded = cfg.State.Downloaded.Load()
					if status.TotalSize > 0 {
						status.Progress = float64(status.Downloaded) * 100 / float64(status.TotalSize)
					}

					// Calculate speed from progress
					downloaded, _, _, sessionElapsed, _, sessionStart := cfg.State.GetProgress()
					sessionDownloaded := downloaded - sessionStart
					if sessionElapsed.Seconds() > 0 && sessionDownloaded > 0 {
						status.Speed = float64(sessionDownloaded) / sessionElapsed.Seconds() / (1024 * 1024)
					}

					// Update status based on state
					if cfg.State.IsPaused() {
						status.Status = "paused"
					} else if cfg.State.Done.Load() {
						status.Status = "completed"
					}
				}

				statuses = append(statuses, status)
			}
		}

		// If no active downloads, get from database
		if len(statuses) == 0 {
			dbDownloads, err := state.ListAllDownloads()
			if err == nil {
				for _, d := range dbDownloads {
					var progress float64
					if d.TotalSize > 0 {
						progress = float64(d.Downloaded) * 100 / float64(d.TotalSize)
					}
					statuses = append(statuses, types.DownloadStatus{
						ID:         d.ID,
						URL:        d.URL,
						Filename:   d.Filename,
						Status:     d.Status,
						TotalSize:  d.TotalSize,
						Downloaded: d.Downloaded,
						Progress:   progress,
					})
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(statuses)
	})

	server := &http.Server{Handler: corsMiddleware(mux)}
	if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
		utils.Debug("HTTP server error: %v", err)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

// DownloadRequest represents a download request from the browser extension
type DownloadRequest struct {
	URL      string `json:"url"`
	Filename string `json:"filename,omitempty"`
	Path     string `json:"path,omitempty"`
}

func handleDownload(w http.ResponseWriter, r *http.Request, defaultOutputDir string) {
	// GET request to query status
	if r.Method == http.MethodGet {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "Missing id parameter", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		// 1. Check GlobalPool first (Active/Queued/Paused)
		if GlobalPool != nil {
			status := GlobalPool.GetStatus(id)
			if status != nil {
				json.NewEncoder(w).Encode(status)
				return
			}
		}

		// 2. Fallback to Database (Completed/Persistent Paused)
		entry, err := state.GetDownload(id)
		if err == nil && entry != nil {
			// Convert to unified DownloadStatus
			var progress float64
			if entry.TotalSize > 0 {
				progress = float64(entry.Downloaded) * 100 / float64(entry.TotalSize)
			} else if entry.Status == "completed" {
				progress = 100.0
			}

			var speed float64
			if entry.Status == "completed" && entry.TimeTaken > 0 {
				// TotalSize (bytes), TimeTaken (ms)
				// Speed = bytes / (ms/1000) / 1024 / 1024 MB/s
				speed = float64(entry.TotalSize) * 1000 / float64(entry.TimeTaken) / (1024 * 1024)
			}

			status := types.DownloadStatus{
				ID:         entry.ID,
				URL:        entry.URL,
				Filename:   entry.Filename,
				TotalSize:  entry.TotalSize,
				Downloaded: entry.Downloaded,
				Progress:   progress,
				Speed:      speed,
				Status:     entry.Status,
			}
			json.NewEncoder(w).Encode(status)
			return
		}

		http.Error(w, "Download not found", http.StatusNotFound)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Load settings once for use throughout the function
	settings, err := config.LoadSettings()
	if err != nil {
		// Fallback to defaults if loading fails (though LoadSettings handles missing file)
		settings = config.DefaultSettings()
	}

	var req DownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if req.URL == "" {
		http.Error(w, "URL is required", http.StatusBadRequest)
		return
	}

	if strings.Contains(req.Path, "..") || strings.Contains(req.Filename, "..") {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	if strings.Contains(req.Filename, "/") || strings.Contains(req.Filename, "\\") {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}
	// Absolute paths are allowed for local tool usage
	// if filepath.IsAbs(req.Path) { ... }

	// Don't default to "." here, let TUI handle it
	// if req.Path == "" {
	// 	req.Path = "."
	// }

	utils.Debug("Received download request: URL=%s, Path=%s", req.URL, req.Path)

	downloadID := uuid.New().String()

	// Use the GlobalPool for both Headless and TUI modes (Unified Backend)
	if GlobalPool == nil {
		// Should not happen if initialized correctly
		http.Error(w, "Server internal error: pool not initialized", http.StatusInternalServerError)
		return
	}

	// Prepare output path
	outPath := req.Path
	if outPath == "" {
		if defaultOutputDir != "" {
			outPath = defaultOutputDir
			_ = os.MkdirAll(outPath, 0755)
		} else {
			if settings.General.DefaultDownloadDir != "" {
				outPath = settings.General.DefaultDownloadDir
				_ = os.MkdirAll(outPath, 0755)
			} else {
				outPath = "."
			}
		}
	}

	// Enforce absolute path to ensure resume works even if CWD changes
	outPath = utils.EnsureAbsPath(outPath)

	// Check settings for extension prompt and duplicates
	// settings already loaded above
	if true {
		// Check for duplicates
		isDuplicate := false
		if GlobalPool.HasDownload(req.URL) {
			isDuplicate = true
		}

		// Logic for prompting:
		// 1. If ExtensionPrompt is enabled
		// 2. OR if WarnOnDuplicate is enabled AND it is a duplicate
		shouldPrompt := settings.General.ExtensionPrompt || (settings.General.WarnOnDuplicate && isDuplicate)

		// Only prompt if we have a UI running (serverProgram != nil)
		if shouldPrompt && serverProgram != nil {
			utils.Debug("Requesting TUI confirmation for: %s (Duplicate: %v)", req.URL, isDuplicate)

			// Send request to TUI
			GlobalProgressCh <- events.DownloadRequestMsg{
				ID:       downloadID,
				URL:      req.URL,
				Filename: req.Filename,
				Path:     outPath, // Use the path we resolved (default or requested)
			}

			w.Header().Set("Content-Type", "application/json")
			// Return 202 Accepted to indicate it's pending approval
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{
				"status":  "pending_approval",
				"message": "Download request sent to TUI for confirmation",
				"id":      downloadID, // ID might change if user modifies it, but useful for tracking
			})
			return
		}
	}

	// Create configuration
	cfg := types.DownloadConfig{
		URL:        req.URL,
		OutputPath: outPath,
		ID:         downloadID,
		Filename:   req.Filename,
		Verbose:    false,
		ProgressCh: GlobalProgressCh, // Shared channel (headless consumer or TUI)
		State:      types.NewProgressState(downloadID, 0),
		// Runtime config loaded from settings
		Runtime: convertRuntimeConfig(settings.ToRuntimeConfig()),
	}

	// Add to pool
	GlobalPool.Add(cfg)

	// Increment active downloads counter (optional, we might rely on pool now)
	atomic.AddInt32(&activeDownloads, 1)

	// In Headless mode, we log to stdout via the progress channel listener
	if serverProgram == nil {
		fmt.Printf("Starting download: %s -> %s (ID: %s)\n", req.URL, outPath, downloadID)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "queued",
		"message": "Download queued successfully",
		"id":      downloadID,
	})
}

// processDownloads handles the logic of adding downloads either to local pool or remote server
func processDownloads(urls []string, outputDir string, port int) {
	// If port > 0, we are sending to a remote server
	if port > 0 {
		for _, url := range urls {
			err := sendToServer(url, outputDir, port)
			if err != nil {
				fmt.Printf("Error adding %s: %v\n", url, err)
			}
		}
		return
	}

	// Internal add (TUI or Headless mode)
	if GlobalPool == nil {
		fmt.Fprintln(os.Stderr, "Error: GlobalPool not initialized")
		return
	}

	settings, err := config.LoadSettings()
	if err != nil {
		settings = config.DefaultSettings()
	}

	for _, url := range urls {
		// Validation
		if url == "" {
			continue
		}

		// Prepare output path
		outPath := outputDir
		if outPath == "" {
			if settings.General.DefaultDownloadDir != "" {
				outPath = settings.General.DefaultDownloadDir
				_ = os.MkdirAll(outPath, 0755)
			} else {
				outPath = "."
			}
		}
		outPath = utils.EnsureAbsPath(outPath)

		// Check for duplicates/extensions if we are in TUI mode (serverProgram != nil)
		// For headless/root direct add, we might skip prompt or auto-approve?
		// For now, let's just add directly if headless, or prompt if TUI is up.

		downloadID := uuid.New().String()

		// If TUI is up (serverProgram != nil), we might want to send a request msg?
		// But processDownloads is called from QUEUE init routine, primarily for CLI args.
		// If CLI args provided, user probably wants them added immediately.

		cfg := types.DownloadConfig{
			URL:        url,
			OutputPath: outPath,
			ID:         downloadID,
			Verbose:    false,
			ProgressCh: GlobalProgressCh,
			State:      types.NewProgressState(downloadID, 0),
			Runtime:    convertRuntimeConfig(settings.ToRuntimeConfig()),
		}

		GlobalPool.Add(cfg)
		atomic.AddInt32(&activeDownloads, 1)

		if serverProgram == nil {
			fmt.Printf("Queued download: %s\n", url)
		}
	}
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.Flags().StringP("batch", "b", "", "File containing URLs to download (one per line)")
	rootCmd.Flags().IntP("port", "p", 0, "Port to listen on (default: 8080 or first available)")
	rootCmd.Flags().StringP("output", "o", "", "Default output directory")
	rootCmd.Flags().Bool("no-resume", false, "Do not auto-resume paused downloads on startup")
	rootCmd.Flags().Bool("exit-when-done", false, "Exit when all downloads complete")
	rootCmd.SetVersionTemplate("Surge version {{.Version}}\n")
}

// initializeGlobalState sets up the environment and configures the engine state and logging
func initializeGlobalState() {
	stateDir := config.GetStateDir()
	logsDir := config.GetLogsDir()

	// Ensure directories exist
	os.MkdirAll(stateDir, 0755)
	os.MkdirAll(logsDir, 0755)

	// Config engine state
	state.Configure(filepath.Join(stateDir, "surge.db"))

	// Config logging
	utils.ConfigureDebug(logsDir)
}

// convertRuntimeConfig converts config.RuntimeConfig to types.RuntimeConfig
func convertRuntimeConfig(rc *config.RuntimeConfig) *types.RuntimeConfig {
	return &types.RuntimeConfig{
		MaxConnectionsPerHost: rc.MaxConnectionsPerHost,
		MaxGlobalConnections:  rc.MaxGlobalConnections,
		UserAgent:             rc.UserAgent,
		MinChunkSize:          rc.MinChunkSize,
		MaxChunkSize:          rc.MaxChunkSize,
		TargetChunkSize:       rc.TargetChunkSize,
		WorkerBufferSize:      rc.WorkerBufferSize,
		MaxTaskRetries:        rc.MaxTaskRetries,
		SlowWorkerThreshold:   rc.SlowWorkerThreshold,
		SlowWorkerGracePeriod: rc.SlowWorkerGracePeriod,
		StallTimeout:          rc.StallTimeout,
		SpeedEmaAlpha:         rc.SpeedEmaAlpha,
	}
}

func resumePausedDownloads() {

	settings, err := config.LoadSettings()
	if err != nil {
		return // Can't check preference
	}

	if !settings.General.AutoResume {
		return
	}

	pausedEntries, err := state.LoadPausedDownloads()
	if err != nil {
		return
	}

	for _, entry := range pausedEntries {
		// Load state to define progress state
		s, err := state.LoadState(entry.URL, entry.DestPath)
		if err != nil {
			continue
		}

		// Reconstruct config
		runtimeConfig := convertRuntimeConfig(settings.ToRuntimeConfig())
		outputPath := filepath.Dir(entry.DestPath)
		// If outputPath is empty or dot, use default
		if outputPath == "" || outputPath == "." {
			outputPath = settings.General.DefaultDownloadDir
		}

		id := entry.ID
		if id == "" {
			id = uuid.New().String()
		}

		// Create progress state
		progState := types.NewProgressState(id, s.TotalSize)
		progState.Downloaded.Store(s.Downloaded)

		cfg := types.DownloadConfig{
			URL:        entry.URL,
			OutputPath: outputPath,
			DestPath:   entry.DestPath,
			ID:         id,
			Filename:   entry.Filename,
			Verbose:    false,
			IsResume:   true,
			ProgressCh: GlobalProgressCh,
			State:      progState,
			Runtime:    runtimeConfig,
		}

		fmt.Printf("Auto-resuming download: %s\n", entry.Filename)
		GlobalPool.Add(cfg)
	}
}

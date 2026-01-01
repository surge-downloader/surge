package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"surge/internal/tui"
	"surge/internal/utils"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

// DownloadRequest represents a download request from the browser extension
type DownloadRequest struct {
	URL      string `json:"url"`
	Filename string `json:"filename,omitempty"`
	Path     string `json:"path,omitempty"`
	Cookies  string `json:"cookies,omitempty"`
}

// serverProgram holds the TUI program instance for server mode
var serverProgram *tea.Program
var serverModel *tui.RootModel

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start Surge in server mode to receive downloads from browser extension",
	Long: `Start Surge in server mode. This runs the TUI and listens on a local port
for download requests from the browser extension.`,
	Run: func(cmd *cobra.Command, args []string) {
		port, _ := cmd.Flags().GetInt("port")

		// Create the initial model
		model := tui.InitialRootModel()
		serverModel = &model

		// Create the TUI program
		serverProgram = tea.NewProgram(model, tea.WithAltScreen())

		// Start HTTP server in background
		go startHTTPServer(port)

		// Run the TUI (blocking)
		if _, err := serverProgram.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Error running TUI: %v\n", err)
			os.Exit(1)
		}
	},
}

func startHTTPServer(port int) {
	mux := http.NewServeMux()

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Download endpoint
	mux.HandleFunc("/download", handleDownload)

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	utils.Debug("Starting HTTP server on %s", addr)

	server := &http.Server{
		Addr:    addr,
		Handler: corsMiddleware(mux),
	}

	if err := server.ListenAndServe(); err != nil {
		utils.Debug("HTTP server error: %v", err)
	}
}

// corsMiddleware adds CORS headers for browser extension requests
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow requests from browser extensions
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		// Handle preflight
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
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

	// Default path
	if req.Path == "" {
		req.Path = "."
	}

	utils.Debug("Received download request: URL=%s, Path=%s", req.URL, req.Path)

	// Send message to TUI to start download
	// We use a custom message type that the TUI will handle
	serverProgram.Send(tui.StartDownloadMsg{
		URL:      req.URL,
		Path:     req.Path,
		Filename: req.Filename,
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "queued",
		"message": "Download request received",
	})
}

func init() {
	serverCmd.Flags().IntP("port", "P", 8080, "Port to listen on for download requests")
	rootCmd.AddCommand(serverCmd)
}

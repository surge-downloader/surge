package download

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/surge-downloader/surge/internal/download/state"
	"github.com/surge-downloader/surge/internal/engine"
	"github.com/surge-downloader/surge/internal/engine/concurrent"
	"github.com/surge-downloader/surge/internal/engine/events"
	"github.com/surge-downloader/surge/internal/engine/single"
	"github.com/surge-downloader/surge/internal/engine/types"
	"github.com/surge-downloader/surge/internal/utils"
)

var probeClient = &http.Client{Timeout: types.ProbeTimeout}

var ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) " +
	"Chrome/120.0.0.0 Safari/537.36"

// ProbeResult contains all metadata from server probe
type ProbeResult struct {
	FileSize      int64
	SupportsRange bool
	Filename      string
	ContentType   string
}

// probeServer has been moved to internal/engine/probe.go

// uniqueFilePath returns a unique file path by appending (1), (2), etc. if the file exists
func uniqueFilePath(path string) string {
	// Check if file exists (both final and incomplete)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if _, err := os.Stat(path + types.IncompleteSuffix); os.IsNotExist(err) {
			return path // Neither exists, use original
		}
	}

	// File exists, generate unique name
	dir := filepath.Dir(path)
	ext := filepath.Ext(path)
	name := strings.TrimSuffix(filepath.Base(path), ext)

	// Check if name already has a counter like "file(1)"
	base := name
	counter := 1

	if len(name) > 3 && name[len(name)-1] == ')' {
		if openParen := strings.LastIndexByte(name, '('); openParen != -1 {
			// Try to parse number between parens
			numStr := name[openParen+1 : len(name)-1]
			if num, err := strconv.Atoi(numStr); err == nil && num > 0 {
				base = name[:openParen]
				counter = num + 1
			}
		}
	}

	for i := 0; i < 100; i++ { // Try next 100 numbers
		candidate := filepath.Join(dir, fmt.Sprintf("%s(%d)%s", base, counter+i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			if _, err := os.Stat(candidate + types.IncompleteSuffix); os.IsNotExist(err) {
				return candidate
			}
		}
	}

	// Fallback: just append a large random number or give up (original behavior essentially gave up or made ugly names)
	// Here we fallback to original behavior of appending if the clean one failed 100 times
	return path
}

// TUIDownload is the main entry point for TUI downloads
func TUIDownload(ctx context.Context, cfg *types.DownloadConfig) error {

	// Probe server once to get all metadata
	probe, err := engine.ProbeServer(ctx, cfg.URL, cfg.Filename)
	if err != nil {
		utils.Debug("Probe failed: %v", err)
		return err
	}

	// Start download timer (exclude probing time)
	start := time.Now()
	defer func() {
		utils.Debug("Download %s completed in %v", cfg.URL, time.Since(start))
	}()

	// Construct proper output path
	destPath := cfg.OutputPath

	// Auto-create output directory if it doesn't exist
	if _, err := os.Stat(cfg.OutputPath); os.IsNotExist(err) {
		if mkErr := os.MkdirAll(cfg.OutputPath, 0755); mkErr != nil {
			utils.Debug("Failed to create output directory: %v", mkErr)
		}
	}

	if info, err := os.Stat(cfg.OutputPath); err == nil && info.IsDir() {
		// Use cfg.Filename if TUI provided one, otherwise use probe.Filename
		filename := probe.Filename
		if cfg.Filename != "" {
			filename = cfg.Filename
		}
		destPath = filepath.Join(cfg.OutputPath, filename)
	}

	// Check if this is a resume (explicitly marked by TUI)
	var savedState *types.DownloadState
	if cfg.IsResume && cfg.DestPath != "" {
		// Resume: use the provided destination path for state lookup
		savedState, _ = state.LoadState(cfg.URL, cfg.DestPath)
	}
	isResume := cfg.IsResume && savedState != nil && len(savedState.Tasks) > 0 && savedState.DestPath != ""

	if isResume {
		// Resume: use saved destination path directly (don't generate new unique name)
		destPath = savedState.DestPath
		utils.Debug("Resuming download, using saved destPath: %s", destPath)
	} else {
		// Fresh download without TUI-provided filename: generate unique filename if file already exists
		destPath = uniqueFilePath(destPath)
	}
	finalFilename := filepath.Base(destPath)
	utils.Debug("Destination path: %s", destPath)

	// Update filename in config so caller (WorkerPool) sees it
	cfg.Filename = finalFilename

	// Send download started message
	if cfg.ProgressCh != nil {
		cfg.ProgressCh <- events.DownloadStartedMsg{
			DownloadID: cfg.ID,
			URL:        cfg.URL,
			Filename:   finalFilename,
			Total:      probe.FileSize,
			DestPath:   destPath,
		}
	}

	// Update shared state
	if cfg.State != nil {
		cfg.State.SetTotalSize(probe.FileSize)
	}

	// Choose downloader based on probe results
	var downloadErr error
	if probe.SupportsRange && probe.FileSize > 0 {
		utils.Debug("Using concurrent downloader")
		d := concurrent.NewConcurrentDownloader(cfg.ID, cfg.ProgressCh, cfg.State, cfg.Runtime)
		downloadErr = d.Download(ctx, cfg.URL, destPath, probe.FileSize, cfg.Verbose)
	} else {
		// Fallback to single-threaded downloader
		utils.Debug("Using single-threaded downloader")
		d := single.NewSingleDownloader(cfg.ID, cfg.ProgressCh, cfg.State, cfg.Runtime)
		downloadErr = d.Download(ctx, cfg.URL, destPath, probe.FileSize, probe.Filename, cfg.Verbose)
	}

	// Only send completion if NO error AND not paused
	isPaused := cfg.State != nil && cfg.State.IsPaused()
	if downloadErr == nil && !isPaused && cfg.ProgressCh != nil {
		cfg.ProgressCh <- events.DownloadCompleteMsg{
			DownloadID: cfg.ID,
			Filename:   finalFilename,
			Elapsed:    time.Since(start),
			Total:      probe.FileSize,
		}
	}

	return downloadErr
}

// Download is the CLI entry point (non-TUI) - convenience wrapper
func Download(ctx context.Context, url, outPath string, verbose bool, progressCh chan<- any, id string) error {
	cfg := types.DownloadConfig{
		URL:        url,
		OutputPath: outPath,
		ID:         id,
		Verbose:    verbose,
		ProgressCh: progressCh,
		State:      nil,
	}
	return TUIDownload(ctx, &cfg)
}

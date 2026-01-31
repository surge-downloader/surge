package concurrent

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/surge-downloader/surge/internal/engine/state"
	"github.com/surge-downloader/surge/internal/engine/types"
	"github.com/surge-downloader/surge/internal/utils"
)

// ConcurrentDownloader handles multi-connection downloads
type ConcurrentDownloader struct {
	ProgressChan chan<- any           // Channel for events (start/complete/error)
	ID           string               // Download ID
	State        *types.ProgressState // Shared state for TUI polling
	activeTasks  map[int]*ActiveTask
	activeMu     sync.Mutex
	URL          string // For pause/resume
	DestPath     string // For pause/resume
	Runtime      *types.RuntimeConfig
	bufPool      sync.Pool
}

// NewConcurrentDownloader creates a new concurrent downloader with all required parameters
func NewConcurrentDownloader(id string, progressCh chan<- any, progState *types.ProgressState, runtime *types.RuntimeConfig) *ConcurrentDownloader {
	return &ConcurrentDownloader{
		ID:           id,
		ProgressChan: progressCh,
		State:        progState,
		activeTasks:  make(map[int]*ActiveTask),
		Runtime:      runtime,
		bufPool: sync.Pool{
			New: func() any {
				// Use configured buffer size
				size := runtime.GetWorkerBufferSize()
				buf := make([]byte, size)
				return &buf
			},
		},
	}
}

// getInitialConnections returns the starting number of connections based on file size
func (d *ConcurrentDownloader) getInitialConnections(fileSize int64) int {
	maxConns := d.Runtime.GetMaxConnectionsPerHost()

	var recConns int
	switch {
	case fileSize < 10*types.MB:
		recConns = 1
	case fileSize < 100*types.MB:
		recConns = 4
	case fileSize < 1*types.GB:
		recConns = 6
	default:
		recConns = 32
	}

	if recConns > maxConns {
		return maxConns
	}
	return recConns
}

// calculateChunkSize determines optimal chunk size
func (d *ConcurrentDownloader) calculateChunkSize(fileSize int64, numConns int) int64 {
	targetChunks := int64(numConns * types.TasksPerWorker)
	chunkSize := fileSize / targetChunks

	// Clamp to min/max from config
	minChunk := d.Runtime.GetMinChunkSize()
	maxChunk := d.Runtime.GetMaxChunkSize()
	targetChunk := d.Runtime.GetTargetChunkSize()

	// If calculating produces something wild, prefer target
	if chunkSize == 0 {
		chunkSize = targetChunk
	}

	if chunkSize < minChunk {
		chunkSize = minChunk
	}
	if chunkSize > maxChunk {
		chunkSize = maxChunk
	}

	// Align to 4KB
	chunkSize = (chunkSize / types.AlignSize) * types.AlignSize
	if chunkSize == 0 {
		chunkSize = types.AlignSize
	}

	return chunkSize
}

// createTasks generates initial task queue from file size and chunk size
func createTasks(fileSize, chunkSize int64) []types.Task {
	if chunkSize <= 0 {
		return nil
	}
	var tasks []types.Task
	for offset := int64(0); offset < fileSize; offset += chunkSize {
		length := chunkSize
		if offset+length > fileSize {
			length = fileSize - offset
		}
		tasks = append(tasks, types.Task{Offset: offset, Length: length})
	}
	return tasks
}

// newConcurrentClient creates an http.Client tuned for concurrent downloads
func (d *ConcurrentDownloader) newConcurrentClient(numConns int) *http.Client {
	// Ensure we have enough connections per host
	maxConns := d.Runtime.GetMaxConnectionsPerHost()
	if numConns > maxConns {
		maxConns = numConns
	}

	transport := &http.Transport{
		// Connection pooling
		MaxIdleConns:        types.DefaultMaxIdleConns,
		MaxIdleConnsPerHost: maxConns + 2, // Slightly more than max to handle bursts
		MaxConnsPerHost:     maxConns,

		// Timeouts to prevent hung connections
		IdleConnTimeout:       types.DefaultIdleConnTimeout,
		TLSHandshakeTimeout:   types.DefaultTLSHandshakeTimeout,
		ResponseHeaderTimeout: types.DefaultResponseHeaderTimeout,
		ExpectContinueTimeout: types.DefaultExpectContinueTimeout,

		// Performance tuning
		DisableCompression: true,  // Files are usually already compressed
		ForceAttemptHTTP2:  false, // FORCE HTTP/1.1 for multiple TCP connections
		TLSNextProto:       make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),

		// Dial settings for TCP reliability
		DialContext: (&net.Dialer{
			Timeout:   types.DialTimeout,
			KeepAlive: types.KeepAliveDuration,
		}).DialContext,
	}

	return &http.Client{
		Transport: transport,
	}
}

// Download downloads a file using multiple concurrent connections
// Uses pre-probed metadata (file size already known)
func (d *ConcurrentDownloader) Download(ctx context.Context, rawurl, destPath string, fileSize int64, verbose bool) error {
	utils.Debug("ConcurrentDownloader.Download: %s -> %s (size: %d)", rawurl, destPath, fileSize)

	// Store URL and path for pause/resume (final path without .surge)
	d.URL = rawurl
	d.DestPath = destPath

	// Working file has .surge suffix until download completes
	workingPath := destPath + types.IncompleteSuffix

	// Create cancellable context for pause support
	downloadCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if d.State != nil {
		d.State.CancelFunc = cancel
	}

	// Determine connections and chunk size
	numConns := d.getInitialConnections(fileSize)
	chunkSize := d.calculateChunkSize(fileSize, numConns)

	// Create tuned HTTP client for concurrent downloads
	client := d.newConcurrentClient(numConns)

	if verbose {
		fmt.Printf("File size: %s, connections: %d, chunk size: %s\n",
			utils.ConvertBytesToHumanReadable(fileSize),
			numConns,
			utils.ConvertBytesToHumanReadable(chunkSize))
	}

	// Create and preallocate output file with .surge suffix
	outFile, err := os.OpenFile(workingPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer outFile.Close()

	// Check for saved state BEFORE truncating (resume case)
	var tasks []types.Task
	savedState, err := state.LoadState(rawurl, destPath)
	isResume := err == nil && savedState != nil && len(savedState.Tasks) > 0

	if isResume {
		// Resume: use saved tasks and restore downloaded counter
		tasks = savedState.Tasks
		if d.State != nil {
			d.State.Downloaded.Store(savedState.Downloaded)
			// Restore elapsed time from previous sessions
			d.State.SetSavedElapsed(time.Duration(savedState.Elapsed))
			// Fix speed spike: sync session start so we don't count previous bytes as new speed
			d.State.SyncSessionStart()
		}
		utils.Debug("Resuming from saved state: %d tasks, %d bytes downloaded", len(tasks), savedState.Downloaded)
	} else {
		// Fresh download: preallocate file and create new tasks
		if err := outFile.Truncate(fileSize); err != nil {
			return fmt.Errorf("failed to preallocate file: %w", err)
		}
		tasks = createTasks(fileSize, chunkSize)
		// Robustness: ensure state counter starts at 0 for fresh download
		if d.State != nil {
			d.State.Downloaded.Store(0)
			d.State.SyncSessionStart()
		}
	}
	queue := NewTaskQueue()
	queue.PushMultiple(tasks)

	// Start time for stats
	startTime := time.Now()

	// Start balancer goroutine for dynamic chunk splitting
	balancerCtx, cancelBalancer := context.WithCancel(downloadCtx)
	defer cancelBalancer()

	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		maxSplits := 50
		splitCount := 0

		for {
			select {
			case <-balancerCtx.Done():
				return
			case <-ticker.C:
				if queue.IdleWorkers() > 0 && splitCount < maxSplits {
					if queue.SplitLargestIfNeeded() {
						splitCount++
						utils.Debug("Balancer: split largest task (total splits: %d)", splitCount)
					} else if queue.Len() == 0 {
						// Try to steal from an active worker
						if d.StealWork(queue) {
							splitCount++
						}
					}
				}
			}
		}
	}()

	// Monitor for completion
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				queue.Close()
				return
			case <-balancerCtx.Done():
				queue.Close()
				return
			case <-ticker.C:
				// Ensure queue is empty (no pending retries) before considering byte count.
				// This protects against cutting off active retries even if byte count seems high (due to overlaps etc).
				if queue.Len() == 0 && (int(queue.IdleWorkers()) == numConns || d.State.Downloaded.Load() >= fileSize) {
					queue.Close()
					return
				}
			}
		}
	}()

	// Health monitor: detect slow workers
	go func() {
		ticker := time.NewTicker(types.HealthCheckInterval) // Fixed: using types constant
		defer ticker.Stop()

		for {
			select {
			case <-balancerCtx.Done():
				return
			case <-ticker.C:
				d.checkWorkerHealth()
			}
		}
	}()

	// Start workers
	var wg sync.WaitGroup
	workerErrors := make(chan error, numConns)

	for i := 0; i < numConns; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			err := d.worker(downloadCtx, workerID, rawurl, outFile, queue, fileSize, startTime, verbose, client)
			if err != nil && err != context.Canceled {
				workerErrors <- err
			}
		}(i)
	}

	// Wait for all workers to complete
	go func() {
		wg.Wait()
		close(workerErrors)
		queue.Close()
	}()

	// Check for errors or pause
	var downloadErr error
	for err := range workerErrors {
		if err != nil {
			downloadErr = err
		}
	}

	// Handle pause: state saved
	if d.State != nil && d.State.IsPaused() {
		// 1. Collect active tasks as remaining work FIRST
		var activeRemaining []types.Task
		d.activeMu.Lock()
		for _, active := range d.activeTasks {
			if remaining := active.RemainingTask(); remaining != nil {
				activeRemaining = append(activeRemaining, *remaining)
			}
		}
		d.activeMu.Unlock()

		// 2. Collect remaining tasks from queue
		remainingTasks := queue.DrainRemaining()
		remainingTasks = append(remainingTasks, activeRemaining...)

		// Calculate Downloaded from remaining tasks (ensures consistency)
		var remainingBytes int64
		for _, task := range remainingTasks {
			remainingBytes += task.Length
		}
		computedDownloaded := fileSize - remainingBytes

		// Calculate total elapsed time
		var totalElapsed time.Duration
		if d.State != nil {
			totalElapsed = d.State.SavedElapsed + time.Since(startTime)
		} else {
			totalElapsed = time.Since(startTime)
		}

		// Save state for resume (use computed value for consistency)
		s := &types.DownloadState{
			URL:        d.URL,
			ID:         d.ID,
			DestPath:   destPath,
			TotalSize:  fileSize,
			Downloaded: computedDownloaded,
			Tasks:      remainingTasks,
			Filename:   filepath.Base(destPath),
			Elapsed:    totalElapsed.Nanoseconds(),
		}
		if err := state.SaveState(d.URL, destPath, s); err != nil {
			utils.Debug("Failed to save pause state: %v", err)
		}

		utils.Debug("Download paused, state saved (Downloaded=%d, RemainingTasks=%d, RemainingBytes=%d)",
			computedDownloaded, len(remainingTasks), remainingBytes)
		return types.ErrPaused // Signal valid pause to caller
	}

	// Handle cancel: context was cancelled but not via Pause() - just exit cleanly
	// The .surge file remains for cleanup by the TUI (which will delete it)
	if downloadCtx.Err() == context.Canceled {
		return nil
	}

	if downloadErr != nil {
		return downloadErr
	}

	// Final sync
	if err := outFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync file: %w", err)
	}

	// Close file before renaming
	outFile.Close()

	// Rename from .surge to final destination
	if err := os.Rename(workingPath, destPath); err != nil {
		// Check for race condition: did someone else already rename it?
		if os.IsNotExist(err) {
			if info, statErr := os.Stat(destPath); statErr == nil && info.Size() == fileSize {
				utils.Debug("Race condition detected: File already exists and has correct size. Treating as success.")
				// Clean up state just in case, though usually done by caller
				_ = state.DeleteState(d.ID, d.URL, destPath)
				return nil
			}
		}
		return fmt.Errorf("failed to rename completed file: %w", err)
	}

	// Delete state file on successful completion
	_ = state.DeleteState(d.ID, d.URL, destPath)

	// Note: Download completion notifications are handled by the TUI via DownloadCompleteMsg

	return nil
}

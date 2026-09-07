package concurrent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SurgeDM/Surge/internal/progress"
	"github.com/SurgeDM/Surge/internal/store"
	"github.com/SurgeDM/Surge/internal/transport"
	"github.com/SurgeDM/Surge/internal/types"
	"github.com/SurgeDM/Surge/internal/utils"
)

// ConcurrentDownloader handles multi-connection downloads
type ConcurrentDownloader struct {
	ProgressChan chan<- types.DownloadEvent // Channel for events (start/complete/error)
	ID           string                     // Download ID
	State        *progress.DownloadProgress // Shared state for TUI polling
	activeTasks  map[int]*ActiveTask
	activeMu     sync.Mutex
	// abandonedRemaining holds ENOSPC in-flight residuals captured off the
	// live queue (no Push during the storm window). saveStateSnapshot unions
	// these with active+queue drains so Resume does not lose the failing range.
	abandonedRemaining []types.Task
	URL                string // For pause/resume
	DestPath           string // For pause/resume
	Runtime            *types.RuntimeConfig
	Limiter            types.ByteLimiter
	RateLimitBps       int64
	RateLimitSet       bool
	TotalSize          int64
	bufPool            sync.Pool
	Headers            map[string]string // Custom HTTP headers from browser (cookies, auth, etc.)
	hostLimiter        *transport.HostRateLimiter
	soft403Mu          sync.Mutex
	soft403Exhaustions int
	soft403Progress    int64
	soft403Since       time.Time
	concurrencyGate    *adaptiveConcurrencyGate
	// completing is set when the completion monitor has decided the download
	// is done. Worker taskCtx cancel is then a completion signal, not a stall.
	completing atomic.Bool
}

const (
	// Each exhaustion already includes the normal per-task retry burn. The
	// confirmation window gives in-flight workers one final chance to advance.
	soft403MaxExhaustions = 16
	soft403ConfirmWindow  = 5 * time.Second
)

// NewConcurrentDownloader creates a new concurrent downloader with all required parameters
func NewConcurrentDownloader(id string, progressCh chan<- types.DownloadEvent, progState *progress.DownloadProgress, runtime *types.RuntimeConfig) *ConcurrentDownloader {
	if runtime == nil {
		runtime = types.DefaultRuntimeConfig()
	}

	return &ConcurrentDownloader{
		ID:           id,
		ProgressChan: progressCh,
		State:        progState,
		activeTasks:  make(map[int]*ActiveTask),
		Runtime:      runtime,
		hostLimiter:  transport.DefaultHostRateLimiter,
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
	maxConns := d.Runtime.GetMaxConnectionsPerDownload()
	minChunkSize := d.Runtime.GetMinChunkSize() // e.g., 1MB or 5MB

	if fileSize <= 0 {
		return 1
	}

	// If caller specified exact worker count, bypass √size heuristic.
	if workers := d.Runtime.GetWorkers(); workers > 0 {
		if workers > maxConns {
			workers = maxConns
		}
		if minChunkSize > 0 {
			maxPossibleChunks := fileSize / minChunkSize
			if maxPossibleChunks < 1 {
				maxPossibleChunks = 1
			}
			if int64(workers) > maxPossibleChunks {
				workers = int(maxPossibleChunks)
			}
		}
		return workers
	}

	// 1. Calculate ideal workers using the Square Root heuristic
	// Convert to float first to avoid integer truncation on small files
	sizeMB := float64(fileSize) / float64(utils.MiB)
	calculatedWorkers := int(math.Round(math.Sqrt(sizeMB)))

	// 2. Hard constraint: Don't create chunks smaller than MinChunkSize
	// If file is 20MB and MinChunk is 10MB, we strictly can't have more than 2 workers
	if minChunkSize > 0 {
		maxPossibleChunks := fileSize / minChunkSize
		if maxPossibleChunks < 1 {
			maxPossibleChunks = 1
		}
		if int64(calculatedWorkers) > maxPossibleChunks {
			calculatedWorkers = int(maxPossibleChunks)
		}
	}

	// 3. Safety Floors and Ceilings
	if calculatedWorkers < 1 {
		return 1
	}
	if calculatedWorkers > maxConns {
		return maxConns
	}

	return calculatedWorkers
}

// ReportMirrorError marks a mirror as having an error in the state
func (d *ConcurrentDownloader) ReportMirrorError(url string) {
	if d.State == nil {
		return
	}

	mirrors := d.State.GetMirrors()
	changed := false
	for i, m := range mirrors {
		if m.URL == url && !m.Error {
			mirrors[i].Error = true
			changed = true
			break
		}
	}

	if changed {
		d.State.SetMirrors(mirrors)
	}
}

func (d *ConcurrentDownloader) shouldEscalate403(now time.Time) bool {
	d.soft403Mu.Lock()
	defer d.soft403Mu.Unlock()

	progress := d.soft403Progress
	if d.State != nil {
		progress = d.State.Bytes.VerifiedProgress.Load()
	}
	if progress != d.soft403Progress {
		d.soft403Progress = progress
		d.soft403Exhaustions = 0
		d.soft403Since = time.Time{}
	}

	if d.soft403Exhaustions < soft403MaxExhaustions {
		d.soft403Exhaustions++
	}
	if d.soft403Exhaustions < soft403MaxExhaustions {
		return false
	}
	if d.soft403Since.IsZero() {
		d.soft403Since = now
		return false
	}
	if now.Before(d.soft403Since.Add(soft403ConfirmWindow)) {
		return false
	}

	// Progress can race the decision above; recheck before stopping healthy peers.
	if d.State != nil {
		progress = d.State.Bytes.VerifiedProgress.Load()
	}
	if progress != d.soft403Progress {
		d.soft403Progress = progress
		d.soft403Exhaustions = 1
		d.soft403Since = time.Time{}
		return false
	}
	return true
}

// calculateChunkSize determines optimal chunk size
func (d *ConcurrentDownloader) calculateChunkSize(fileSize int64, numConns int) int64 {
	// Safety check
	if numConns <= 0 {
		return d.Runtime.GetMinChunkSize() // Fallback
	}

	chunkSize := fileSize / int64(numConns)

	// Clamp to min from config (but not max - we want large chunks)
	minChunk := d.Runtime.GetMinChunkSize()

	if chunkSize < minChunk {
		chunkSize = minChunk
	}

	// Align to 4KB
	chunkSize = (chunkSize / types.AlignSize) * types.AlignSize
	if chunkSize == 0 {
		chunkSize = types.AlignSize
	}

	return chunkSize
}

// determineChunkSize decides the strategy (Sequential vs Parallel)
func (d *ConcurrentDownloader) determineChunkSize(fileSize int64, numConns int) int64 {
	if d.Runtime.SequentialDownload {
		// Sequential mode: Use small fixed chunks (MinChunkSize) to ensure strict ordering
		chunkSize := d.Runtime.GetMinChunkSize()
		if chunkSize <= 0 {
			chunkSize = 2 * utils.MiB // Default 2MB if not configured
		}
		// Align to 4KB
		chunkSize = (chunkSize / types.AlignSize) * types.AlignSize
		if chunkSize == 0 {
			chunkSize = types.AlignSize
		}
		return chunkSize
	}

	// Parallel mode: Use large shards
	return d.calculateChunkSize(fileSize, numConns)
}

// createTasks generates initial task queue from file size and chunk size
func createTasks(fileSize, chunkSize int64) []types.Task {
	if chunkSize <= 0 {
		return nil
	}

	// preallocate slice capacity
	count := (fileSize + chunkSize - 1) / chunkSize
	tasks := make([]types.Task, 0, int(count))

	for offset := int64(0); offset < fileSize; offset += chunkSize {
		length := chunkSize
		if offset+length > fileSize {
			length = fileSize - offset
		}
		tasks = append(tasks, types.Task{Offset: offset, Length: length})
	}
	return tasks
}

// createInitialTasks keeps parallel downloads within their worker request
// budget by assigning any alignment remainder to the last primary range.
func createInitialTasks(fileSize, chunkSize int64, maxTasks int) []types.Task {
	tasks := createTasks(fileSize, chunkSize)
	if maxTasks <= 0 || len(tasks) <= maxTasks {
		return tasks
	}

	tasks[maxTasks-1].Length = fileSize - tasks[maxTasks-1].Offset
	return tasks[:maxTasks]
}

func (d *ConcurrentDownloader) applyClientSettings(client *http.Client) {
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return types.ErrMaxRedirects
		}
		if len(via) > 0 {
			utils.CopyRedirectHeaders(req, via[0])
		}
		return nil
	}
}

// Download downloads a file using multiple concurrent connections
// Uses pre-probed metadata (file size already known)
func (d *ConcurrentDownloader) Download(ctx context.Context, rawurl string, candidateMirrors []string, activeMirrors []string, destPath string, fileSize int64) error {
	utils.Debug("ConcurrentDownloader.Download: %s -> %s (size: %d, mirrors: %d)", rawurl, destPath, fileSize, len(activeMirrors))

	d.soft403Mu.Lock()
	d.soft403Exhaustions = 0
	d.soft403Progress = 0
	d.soft403Since = time.Time{}
	d.soft403Mu.Unlock()
	d.completing.Store(false)

	if d.hostLimiter == nil {
		d.hostLimiter = transport.DefaultHostRateLimiter
	}

	d.initMirrorStatus(rawurl, candidateMirrors, activeMirrors, destPath)

	workingPath := destPath + types.IncompleteSuffix
	downloadCtx, cancel := context.WithCancel(ctx)

	if d.State != nil {
		d.State.SetCancelFunc(cancel)
	}

	client, httpTransport := d.setupNetwork()
	// Release transport back to the pool ONLY after all helpers and workers are joined (LIFO: runs last)
	defer transport.DefaultNetworkPool.ReleaseTransport(httpTransport)

	// Helper synchronization for monitors and balancer
	var wgHelpers sync.WaitGroup
	// Ensure we wait for helpers to finish; run wait AFTER cancel (LIFO: Wait runs second, cancel runs first)
	defer wgHelpers.Wait()
	defer cancel()

	// Ensure we have the total file size
	if fileSize <= 0 {
		var err error
		fileSize, err = d.bootstrapMetadata(downloadCtx, client, rawurl)
		if err != nil {
			return err
		}
	}
	d.TotalSize = fileSize

	// Load saved state early to determine remaining size for connection count heuristic
	savedState, err := store.LoadState(d.URL, destPath)
	isResume := err == nil && savedState != nil && len(savedState.Tasks) > 0

	effectiveSizeForWorkers := d.getEffectiveSizeForWorkers(fileSize, savedState, isResume)

	numConns := d.getInitialConnections(effectiveSizeForWorkers)
	d.concurrencyGate = newAdaptiveConcurrencyGate(numConns, d.Runtime.GetAdaptiveConcurrencyInterval())
	if d.State != nil {
		d.State.RateLimited.Store(false)
	}
	chunkSize := d.determineChunkSize(fileSize, numConns)

	workerMirrors := d.getWorkerMirrors(activeMirrors)

	// Prewarming helps a fresh transfer discover usable connections. A resumed
	// transfer may be recovering from a host cooldown, so extra probe requests
	// only make the rate-limit situation worse.
	hedgeCount := d.Runtime.GetDialHedgeCount()
	if hedgeCount > 0 && !isResume {
		d.prewarmConnections(downloadCtx, client, numConns, hedgeCount, workerMirrors)
	}

	// Open existing output file with .surge suffix (must be created by processing layer)
	outFile, err := os.OpenFile(workingPath, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("failed to open working file: %w", err)
	}
	defer func() {
		if outFile != nil {
			_ = outFile.Close()
		}
	}()

	// Initialize chunk visualization (must happen BEFORE setupTasks so RestoreBitmap can overwrite it)
	if d.State != nil {
		d.State.InitBitmap(fileSize, chunkSize)
	}

	tasks, err := d.setupTasks(destPath, fileSize, chunkSize, numConns, outFile, savedState, isResume)
	if err != nil {
		return err
	}

	queue := NewTaskQueue()
	queue.PushMultiple(tasks)

	// Start monitoring and balancing helpers
	d.startHelpers(downloadCtx, &wgHelpers, queue, fileSize, numConns)

	// Execute download workers
	downloadErr := d.executeWorkers(downloadCtx, cancel, client, outFile, queue, fileSize, workerMirrors, numConns)

	// Handle pause request: must return types.ErrPaused to prevent finalization
	if d.State != nil && d.State.IsPaused() {
		pauseErr := d.handlePause(destPath, fileSize, queue, candidateMirrors)
		if pauseErr == nil {
			// Pause was requested at completion boundary, so handlePause finalized it.
			return d.syncFile(outFile)
		}
		return pauseErr
	}

	if downloadErr != nil {
		// Save state so that retries (like rate limit backoffs) resume from the correct progress
		if d.State != nil && !errors.Is(downloadErr, context.Canceled) && !errors.Is(downloadErr, context.DeadlineExceeded) {
			if saveErr := d.saveStateSnapshot(destPath, fileSize, queue, candidateMirrors, false); saveErr != nil {
				return errors.Join(downloadErr, fmt.Errorf("save resume state: %w", saveErr))
			}
		}
		return downloadErr
	}
	if downloadCtx.Err() != nil {
		return downloadCtx.Err()
	}

	// Note: Download completion notifications are handled by the TUI via DownloadCompleteMsg
	return d.syncFile(outFile)
}

func (d *ConcurrentDownloader) initMirrorStatus(rawurl string, candidateMirrors []string, activeMirrors []string, destPath string) {
	d.URL = rawurl
	d.DestPath = destPath

	if d.State == nil {
		return
	}

	d.State.SetURL(rawurl)
	d.State.SetDestPath(destPath)

	var statuses []types.MirrorStatus
	statuses = append(statuses, types.MirrorStatus{URL: rawurl, Active: true})

	activeMap := make(map[string]bool)
	for _, m := range activeMirrors {
		activeMap[m] = true
		if m != rawurl {
			statuses = append(statuses, types.MirrorStatus{URL: m, Active: true})
		}
	}

	for _, m := range candidateMirrors {
		if !activeMap[m] && m != rawurl {
			statuses = append(statuses, types.MirrorStatus{URL: m, Active: false, Error: true})
		}
	}

	d.State.SetMirrors(statuses)
}

func (d *ConcurrentDownloader) setupNetwork() (*http.Client, *http.Transport) {
	var proxyURL, customDNS string
	if d.Runtime != nil {
		proxyURL = d.Runtime.ProxyURL
		customDNS = d.Runtime.CustomDNS
	}

	// The worker pool enforces the per-download request budget. Keep the shared
	// transport ceiling process-wide so downloads with identical settings do not
	// accidentally share one per-download MaxConnsPerHost allowance.
	httpTransport := transport.DefaultNetworkPool.AcquireTransport(proxyURL, customDNS, 0)
	client := &http.Client{Transport: httpTransport}
	d.applyClientSettings(client)
	return client, httpTransport
}

func (d *ConcurrentDownloader) getWorkerMirrors(activeMirrors []string) []string {
	mirrors := make([]string, 0, len(activeMirrors)+1)
	mirrors = append(mirrors, d.URL)
	for _, v := range activeMirrors {
		if v != d.URL {
			mirrors = append(mirrors, v)
		}
	}
	return mirrors
}

func (d *ConcurrentDownloader) getEffectiveSizeForWorkers(fileSize int64, savedState *types.DownloadRecord, isResume bool) int64 {
	if isResume && savedState != nil && savedState.TotalSize > 0 {
		eff := savedState.TotalSize - savedState.Downloaded
		if eff < 0 {
			return 0
		}
		return eff
	}
	return fileSize
}

func (d *ConcurrentDownloader) setupTasks(destPath string, fileSize, chunkSize int64, numConns int, outFile *os.File, savedState *types.DownloadRecord, isResume bool) ([]types.Task, error) {
	if isResume {
		if d.State != nil {
			d.State.SetSavedElapsed(time.Duration(savedState.Elapsed))

			if len(savedState.ChunkBitmap) > 0 && savedState.ActualChunkSize > 0 {
				d.State.RestoreBitmap(savedState.ChunkBitmap, savedState.ActualChunkSize)
				d.State.RecalculateProgress(savedState.Tasks)
				downloaded := savedState.Downloaded
				if vp := d.State.Bytes.VerifiedProgress.Load(); downloaded < vp {
					downloaded = vp
				}
				d.State.Bytes.Downloaded.Store(downloaded)
				utils.Debug("Restored chunk map: size %d", savedState.ActualChunkSize)
			} else {
				d.State.Bytes.Downloaded.Store(savedState.Downloaded)
				d.State.Bytes.VerifiedProgress.Store(savedState.Downloaded)
			}
			d.State.SyncSessionStart()
		}
		utils.Debug("Resuming from saved state: %d tasks, %d bytes downloaded", len(savedState.Tasks), savedState.Downloaded)
		return savedState.Tasks, nil
	}

	if err := outFile.Truncate(fileSize); err != nil {
		return nil, fmt.Errorf("failed to preallocate file: %w", err)
	}
	if d.State != nil {
		d.State.Bytes.Downloaded.Store(0)
		d.State.SyncSessionStart()
	}
	if d.Runtime.SequentialDownload {
		return createTasks(fileSize, chunkSize), nil
	}
	return createInitialTasks(fileSize, chunkSize, numConns), nil
}

func (d *ConcurrentDownloader) startHelpers(ctx context.Context, wg *sync.WaitGroup, queue *TaskQueue, fileSize int64, numConns int) {
	if d.concurrencyGate != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.concurrencyGate.runRecovery(ctx, func(oldCap, newCap int, recovered bool) {
				if newCap > oldCap {
					utils.Debug("Adaptive concurrency: increased cap from %d to %d after healthy window", oldCap, newCap)
				}
				if recovered && d.State != nil {
					d.State.RateLimited.Store(false)
				}
			})
		}()
	}

	// Balancer for dynamic chunk splitting and work stealing
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.runBalancer(ctx, queue)
	}()

	// Monitor for download completion
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.runCompletionMonitor(ctx, queue, fileSize, numConns)
	}()

	// Health monitor for detecting slow workers
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.runHealthMonitor(ctx)
	}()
}

func (d *ConcurrentDownloader) runBalancer(ctx context.Context, queue *TaskQueue) {
	idleCh := queue.WorkerIdleCh()

	for {
		select {
		case <-ctx.Done():
			return
		case <-idleCh:
			// A worker just went idle. Drain: keep trying to give idle workers
			// something to do until we run out of idle workers or work to assign.
			for queue.IdleWorkers() > 0 {
				didWork := false
				if queue.Len() == 0 {
					if d.StealWork(queue) {
						didWork = true
					}
				}
				if !didWork {
					break
				}
			}
		}
	}
}

func (d *ConcurrentDownloader) runCompletionMonitor(ctx context.Context, queue *TaskQueue, fileSize int64, numConns int) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			queue.Close()
			return
		case <-ticker.C:
			if d.State != nil && d.State.Bytes.VerifiedProgress.Load() >= fileSize {
				// Mark first so a cancelled worker does not treat this as a
				// health stall and Push after Close.
				d.completing.Store(true)
				d.activeMu.Lock()
				for _, at := range d.activeTasks {
					if at != nil && at.Cancel != nil {
						at.Cancel()
					}
				}
				d.activeMu.Unlock()
				queue.Close()
				// Close only unblocks Pop waiters; already-queued leftovers
				// would otherwise restick workers after 100%.
				queue.DrainRemaining()
				return
			}
			parked := int64(0)
			if d.concurrencyGate != nil {
				parked = d.concurrencyGate.parkedWorkers()
			}
			isDone := queue.Len() == 0 && int(queue.IdleWorkers()+parked) == numConns
			if isDone && d.State != nil && d.State.Bytes.VerifiedProgress.Load() < fileSize {
				isDone = false
			}
			if isDone {
				d.completing.Store(true)
				d.activeMu.Lock()
				for _, at := range d.activeTasks {
					if at != nil && at.Cancel != nil {
						at.Cancel()
					}
				}
				d.activeMu.Unlock()
				queue.Close()
				return
			}
		}
	}
}

func (d *ConcurrentDownloader) runHealthMonitor(ctx context.Context) {
	ticker := time.NewTicker(types.HealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.checkWorkerHealth()
		}
	}
}

func (d *ConcurrentDownloader) executeWorkers(ctx context.Context, cancel context.CancelFunc, client *http.Client, outFile *os.File, queue *TaskQueue, fileSize int64, workerMirrors []string, numConns int) error {
	var wg sync.WaitGroup
	workerErrors := make(chan error, numConns)

	// Start workers
	for i := 0; i < numConns; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			err := d.worker(ctx, workerID, workerMirrors, outFile, queue, fileSize, client)
			if err != nil && !errors.Is(err, context.Canceled) {
				workerErrors <- err
				cancel()
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
	seenErrors := make(map[string]bool)
	for err := range workerErrors {
		if err != nil {
			errStr := err.Error()
			if !seenErrors[errStr] {
				downloadErr = errors.Join(downloadErr, err)
				seenErrors[errStr] = true
			}
		}
	}
	return downloadErr
}

func (d *ConcurrentDownloader) handlePause(destPath string, fileSize int64, queue *TaskQueue, candidateMirrors []string) error {
	return d.saveStateSnapshot(destPath, fileSize, queue, candidateMirrors, true)
}

func (d *ConcurrentDownloader) saveStateSnapshot(destPath string, fileSize int64, queue *TaskQueue, candidateMirrors []string, emitPauseEvent bool) error {
	// 1. Collect active tasks as remaining work FIRST, then drain abandoned
	// residuals (off-queue stashes from terminal-error workers), then drain
	// the live queue.
	var activeRemaining []types.Task
	d.activeMu.Lock()
	for _, active := range d.activeTasks {
		if remaining := active.RemainingTask(); remaining != nil {
			activeRemaining = append(activeRemaining, *remaining)
		}
	}
	abandoned := d.abandonedRemaining
	d.abandonedRemaining = nil
	d.activeMu.Unlock()

	// 2. Collect remaining tasks from queue
	allTasks := append(append(append([]types.Task(nil), activeRemaining...), abandoned...), queue.DrainRemaining()...)

	remainingTasks := make([]types.Task, 0, len(allTasks))
	var remainingBytes int64
	for _, task := range allTasks {
		remainingTasks = append(remainingTasks, task)
		remainingBytes += task.Length
	}

	if remainingBytes == 0 {
		if d.State == nil {
			return nil
		}
		if d.State.Bytes.VerifiedProgress.Load() >= fileSize {
			utils.Debug("Download state save requested at completion boundary; finalizing as completed")
			d.State.Resume()
			_, _ = d.State.FinalizeSession(fileSize)
			return nil
		}
		utils.Debug("Download pause at remainingBytes=0 but VP=%d < fileSize=%d; saving state for resume",
			d.State.Bytes.VerifiedProgress.Load(), fileSize)
	}

	computedDownloaded := fileSize - remainingBytes
	if remainingBytes == 0 {
		computedDownloaded = d.State.Bytes.VerifiedProgress.Load()
	}
	if d.State == nil {
		if emitPauseEvent {
			return types.ErrPaused
		}
		return nil
	}

	// Calculate total elapsed time
	totalElapsed := d.State.FinalizePauseSession(computedDownloaded)

	// Get persisted bitmap data
	bitmap, _, _, chunkSize, _ := d.State.GetBitmapSnapshot(false)

	var rateLimit int64
	var rateLimitSet bool
	if d.State != nil {
		rateLimit, rateLimitSet = d.State.GetRateLimit()
	} else {
		rateLimit, rateLimitSet = d.RateLimitBps, d.RateLimitSet
	}

	// Save state for resume (use computed value for consistency)
	s := &types.DownloadRecord{
		URL:             d.URL,
		ID:              d.ID,
		DestPath:        destPath,
		TotalSize:       fileSize,
		Downloaded:      computedDownloaded,
		Tasks:           remainingTasks,
		Filename:        filepath.Base(destPath),
		Elapsed:         totalElapsed.Nanoseconds(),
		Mirrors:         candidateMirrors,
		ChunkBitmap:     bitmap,
		ActualChunkSize: chunkSize,
		RateLimit:       rateLimit,
		RateLimitSet:    rateLimitSet,
		Workers:         d.Runtime.GetWorkers(),
		MinChunkSize:    d.Runtime.GetMinChunkSize(),
	}

	d.State.SetPendingResumeState(s)
	if err := store.SaveStateWithOptions(d.URL, destPath, s, store.SaveStateOptions{SkipFileHash: true}); err != nil {
		return fmt.Errorf("save state snapshot: %w", err)
	}
	utils.Debug("Saved progress state snapshot (Downloaded=%d, RemainingTasks=%d)", s.Downloaded, len(s.Tasks))

	if emitPauseEvent {
		if d.ProgressChan != nil {
			event := types.DownloadEvent{
				Type:         types.EventPaused,
				DownloadID:   d.ID,
				Filename:     filepath.Base(destPath),
				Downloaded:   computedDownloaded,
				State:        s,
				RateLimit:    rateLimit,
				RateLimitSet: rateLimitSet,
				Workers:      d.Runtime.GetWorkers(),
				MinChunkSize: d.Runtime.GetMinChunkSize(),
			}
			select {
			case d.ProgressChan <- event:
				// A consumed pending snapshot is the delivery handshake used by the
				// scheduler to avoid sending a duplicate fallback event.
				d.State.TakePendingResumeState()
			default:
				utils.Debug("Pause event queue full; scheduler will deliver saved state")
			}
		}
		utils.Debug("Download paused, state saved (Downloaded=%d, RemainingTasks=%d, RemainingBytes=%d)",
			computedDownloaded, len(remainingTasks), remainingBytes)
		return types.ErrPaused
	}

	return nil
}

func (d *ConcurrentDownloader) syncFile(outFile *os.File) error {
	if outFile == nil {
		return nil
	}
	if err := outFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync file: %w", err)
	}
	return nil
}

func (d *ConcurrentDownloader) bootstrapMetadata(ctx context.Context, client *http.Client, rawurl string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create concurrent bootstrap request: %w", err)
	}

	// Preserve auth/session cookies from the browser across the bootstrap request;
	// the server may reject unauthenticated probes with 401/403.
	for key, val := range d.Headers {
		if key != "Range" {
			req.Header.Set(key, val)
		}
	}
	// Range must come after custom headers so a caller-supplied Range can't override the probe byte
	req.Header.Set("User-Agent", d.Runtime.GetUserAgent())
	req.Header.Set("Range", "bytes=0-0")

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to bootstrap concurrent download: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32*utils.KiB))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusPartialContent {
		if types.IsPermanentHTTPStatus(resp.StatusCode) {
			return 0, fmt.Errorf("concurrent bootstrap requires 206 response, got %d: %w", resp.StatusCode, types.ErrPermanentHTTP)
		}
		return 0, fmt.Errorf("concurrent bootstrap requires 206 response, got %d", resp.StatusCode)
	}

	contentRange := resp.Header.Get("Content-Range")
	if contentRange == "" {
		return 0, fmt.Errorf("concurrent bootstrap missing Content-Range header")
	}
	idx := strings.LastIndex(contentRange, "/")
	if idx == -1 || idx+1 >= len(contentRange) {
		return 0, fmt.Errorf("concurrent bootstrap invalid Content-Range header: %q", contentRange)
	}

	sizeStr := contentRange[idx+1:]
	if sizeStr == "*" {
		return 0, fmt.Errorf("concurrent bootstrap returned unknown size")
	}

	fileSize, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil || fileSize <= 0 {
		return 0, fmt.Errorf("concurrent bootstrap invalid file size %q", sizeStr)
	}

	if d.State != nil {
		d.State.SetTotalSize(fileSize)
	}

	return fileSize, nil
}

// prewarmConnections fires off concurrent pings to the mirrors to populate the connection pool
func (d *ConcurrentDownloader) prewarmConnections(ctx context.Context, client *http.Client, numRequired, hedgeCount int, mirrors []string) {
	totalToStart := numRequired + hedgeCount
	if totalToStart > 128 { // Safety cap
		totalToStart = 128
	}

	// Channel to signal when a connection is ready (handshake complete)
	ready := make(chan struct{}, totalToStart)

	// Create a sub-context for the pings so we can stop them once we have enough
	pingCtx, cancelPings := context.WithCancel(ctx)
	defer cancelPings()

	for i := 0; i < totalToStart; i++ {
		go func(idx int) {
			// Round-robin mirrors
			mirror := mirrors[idx%len(mirrors)]

			// Use a fast Range request to ensure the handshake completes
			req, err := http.NewRequestWithContext(pingCtx, http.MethodGet, mirror, nil)
			if err != nil {
				return
			}

			// Forward custom headers (essential for authenticated mirrors)
			for key, val := range d.Headers {
				if key != "Range" {
					req.Header.Set(key, val)
				}
			}

			// Ensure User-Agent and Range are set
			if req.Header.Get("User-Agent") == "" {
				req.Header.Set("User-Agent", d.Runtime.GetUserAgent())
			}
			req.Header.Set("Range", "bytes=0-0")

			// Perform dial + request
			resp, err := client.Do(req)
			if err != nil {
				return
			}

			// Drain body and close to return connection to idle pool, then signal readiness.
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			ready <- struct{}{}
		}(i)
	}

	// Wait until we have enough ready connections OR we hit a timeout
	completed := 0
	timeout := time.After(types.DialTimeout) // Use standard dial timeout for the whole batch

	for completed < numRequired {
		select {
		case <-ready:
			completed++
		case <-timeout:
			utils.Debug("Pre-warming timed out after %d/%d connections", completed, numRequired)
			return
		case <-ctx.Done():
			return
		}
	}

	utils.Debug("Pre-warming complete: %d connections hot", completed)
	// Remaining pings will be cancelled by defer cancelPings()
}

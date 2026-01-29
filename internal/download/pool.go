package download

import (
	"context"
	"sync"
	"time"

	"github.com/surge-downloader/surge/internal/engine/events"
	"github.com/surge-downloader/surge/internal/engine/state"
	"github.com/surge-downloader/surge/internal/engine/types"
	"github.com/surge-downloader/surge/internal/utils"
)

// activeDownload tracks a download that's currently running
type activeDownload struct {
	config types.DownloadConfig
	cancel context.CancelFunc
}

type WorkerPool struct {
	taskChan     chan types.DownloadConfig
	progressCh   chan<- any
	downloads    map[string]*activeDownload      // Track active downloads for pause/resume
	queued       map[string]types.DownloadConfig // Track queued downloads
	mu           sync.RWMutex
	wg           sync.WaitGroup //We use this to wait for all active downloads to pause before exiting the program
	maxDownloads int
}

func NewWorkerPool(progressCh chan<- any, maxDownloads int) *WorkerPool {
	if maxDownloads < 1 {
		maxDownloads = 3 // Default to 3 if invalid
	}
	pool := &WorkerPool{
		taskChan:     make(chan types.DownloadConfig, 100), //We make it buffered to avoid blocking add
		progressCh:   progressCh,
		downloads:    make(map[string]*activeDownload),
		queued:       make(map[string]types.DownloadConfig),
		maxDownloads: maxDownloads,
	}
	for i := 0; i < maxDownloads; i++ {
		go pool.worker()
	}
	return pool
}

// Add adds a new download task to the pool
func (p *WorkerPool) Add(cfg types.DownloadConfig) {
	p.mu.Lock()
	p.queued[cfg.ID] = cfg
	p.mu.Unlock()
	p.taskChan <- cfg
}

// HasDownload checks if a download with the given URL already exists
func (p *WorkerPool) HasDownload(url string) bool {
	p.mu.RLock()
	// Check active downloads
	for _, ad := range p.downloads {
		if ad.config.URL == url {
			p.mu.RUnlock()
			return true
		}
	}
	p.mu.RUnlock()

	// Check persistent store (completed/queued/paused)
	// We do this outside the lock to avoid holding it during DB query
	exists, err := state.CheckDownloadExists(url)
	if err == nil && exists {
		return true
	}

	return false
}

// ActiveCount returns the number of currently active (downloading/pausing) downloads
func (p *WorkerPool) ActiveCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	count := 0
	for _, ad := range p.downloads {
		// Count if not completed and not fully paused
		if ad.config.State != nil && !ad.config.State.Done.Load() && !ad.config.State.IsPaused() {
			count++
		}
	}
	// Also count queued
	count += len(p.queued)
	return count
}

// Pause pauses a specific download by ID
func (p *WorkerPool) Pause(downloadID string) {
	p.mu.RLock()
	ad, exists := p.downloads[downloadID]
	p.mu.RUnlock()

	if !exists || ad == nil {
		return
	}

	// Set paused flag and cancel context
	if ad.config.State != nil {
		// Idempotency: If already pausing or paused, do nothing
		if ad.config.State.IsPausing() || ad.config.State.IsPaused() {
			return
		}
		ad.config.State.SetPausing(true) // Mark as transitioning to pause
		ad.config.State.Pause()
	}

	// Send pause message
	if p.progressCh != nil {
		downloaded := int64(0)
		if ad.config.State != nil {
			downloaded = ad.config.State.Downloaded.Load()
		}
		p.progressCh <- events.DownloadPausedMsg{
			DownloadID: downloadID,
			Downloaded: downloaded,
		}
	}
}

// PauseAll pauses all active downloads (for graceful shutdown)
func (p *WorkerPool) PauseAll() {
	p.mu.RLock()
	ids := make([]string, 0, len(p.downloads)) //This stores the uuids of the downloads to be paused
	for id, ad := range p.downloads {
		// Only pause downloads that are actually active (not already paused or done or pausing)
		if ad != nil && ad.config.State != nil && !ad.config.State.IsPaused() && !ad.config.State.Done.Load() && !ad.config.State.IsPausing() {
			ids = append(ids, id)
		}
	}
	p.mu.RUnlock()

	for _, id := range ids {
		p.Pause(id)
	}
}

// Cancel cancels and removes a download by ID
func (p *WorkerPool) Cancel(downloadID string) {
	p.mu.Lock()
	ad, exists := p.downloads[downloadID]
	if exists {
		delete(p.downloads, downloadID)
	}
	p.mu.Unlock()

	if !exists || ad == nil {
		return
	}

	// Cancel the context to stop workers
	if ad.cancel != nil {
		ad.cancel()
	}

	// Mark as done to stop polling
	if ad.config.State != nil {
		ad.config.State.Done.Store(true)
	}
}

// Resume resumes a paused download by ID
func (p *WorkerPool) Resume(downloadID string) {
	p.mu.RLock()
	ad, exists := p.downloads[downloadID]
	p.mu.RUnlock()

	if !exists || ad == nil {
		return
	}

	// Prevent race: Don't resume if still pausing
	if ad.config.State != nil && ad.config.State.IsPausing() {
		utils.Debug("Resume ignored: download %s is still pausing", downloadID)
		return
	}

	// Idempotency: If already running (not paused), do nothing
	if ad.config.State != nil && !ad.config.State.IsPaused() {
		utils.Debug("Resume ignored: download %s is already running", downloadID)
		return
	}

	// Clear paused flag and reset session start to avoid speed spikes/dips checks
	if ad.config.State != nil {
		ad.config.State.Resume()
		ad.config.State.SyncSessionStart()
	}

	// Re-queue the download
	ad.config.IsResume = true
	p.Add(ad.config)

	// Send resume message
	if p.progressCh != nil {
		p.progressCh <- events.DownloadResumedMsg{
			DownloadID: downloadID,
		}
	}
}

func (p *WorkerPool) worker() {
	for cfg := range p.taskChan {
		p.wg.Add(1)
		// Create cancellable context
		ctx, cancel := context.WithCancel(context.Background())

		// Register active download
		ad := &activeDownload{
			config: cfg,
			cancel: cancel,
		}
		p.mu.Lock()
		delete(p.queued, cfg.ID)
		p.downloads[cfg.ID] = ad
		p.mu.Unlock()

		err := TUIDownload(ctx, &ad.config)

		// Logic:
		// 1. If Pause() was called: State.IsPaused() is true. We keep the task in p.downloads (so it can be resumed).
		// 2. If finished/error: We remove from p.downloads.

		isPaused := ad.config.State != nil && ad.config.State.IsPaused()

		// Clear "Pausing" transition state now that worker has exited
		if ad.config.State != nil {
			ad.config.State.SetPausing(false)
		}

		if isPaused {
			utils.Debug("WorkerPool: Download %s paused cleanly", cfg.ID)
			// If paused, we keep it in downloads map for potential resume
		} else if err != nil {
			if cfg.State != nil {
				cfg.State.SetError(err)
			}
			if p.progressCh != nil {
				p.progressCh <- events.DownloadErrorMsg{DownloadID: cfg.ID, Err: err}
			}
			// Clean up errored download from tracking (don't save to .surge)
			p.mu.Lock()
			delete(p.downloads, cfg.ID)
			p.mu.Unlock()

		} else if !isPaused {
			// Only mark as done if not paused
			if cfg.State != nil {
				cfg.State.Done.Store(true)
			}
			// Note: DownloadCompleteMsg is sent by the progress reporter when it detects Done=true

			// Clean up from tracking
			p.mu.Lock()
			delete(p.downloads, cfg.ID)
			p.mu.Unlock()
		}
		// If paused, we keep it in downloads map for potential resume
		p.wg.Done()
	}
}

// GetStatus returns the status of an active download
func (p *WorkerPool) GetStatus(id string) *types.DownloadStatus {
	p.mu.RLock()
	ad, exists := p.downloads[id]
	qCfg, qExists := p.queued[id]
	p.mu.RUnlock()

	if !exists && !qExists {
		return nil
	}

	if qExists {
		return &types.DownloadStatus{
			ID:         id,
			URL:        qCfg.URL,
			Filename:   qCfg.Filename,
			Status:     "queued",
			Downloaded: 0,
			TotalSize:  0, // Metadata not yet fetched
		}
	}

	state := ad.config.State
	if state == nil {
		return nil
	}

	status := &types.DownloadStatus{
		ID:         id,
		URL:        ad.config.URL,
		Filename:   ad.config.Filename,
		TotalSize:  state.TotalSize,
		Downloaded: state.Downloaded.Load(),
		Status:     "downloading",
	}

	if ad.config.State.IsPausing() {
		status.Status = "pausing"
	} else if ad.config.State.IsPaused() {
		status.Status = "paused"
	} else if state.Done.Load() {
		status.Status = "completed"
	}

	if err := state.GetError(); err != nil {
		status.Status = "error"
		status.Error = err.Error()
	}

	// Calculate progress
	if status.TotalSize > 0 {
		status.Progress = float64(status.Downloaded) * 100 / float64(status.TotalSize)
	}

	// Calculate speed (MB/s)
	downloaded, _, _, sessionElapsed, _, sessionStart := state.GetProgress()
	sessionDownloaded := downloaded - sessionStart
	if sessionElapsed.Seconds() > 0 && sessionDownloaded > 0 {
		bytesPerSec := float64(sessionDownloaded) / sessionElapsed.Seconds()
		status.Speed = bytesPerSec / (1024 * 1024)
	}

	return status
}

// GracefulShutdown pauses all downloads and waits for them to save state
func (p *WorkerPool) GracefulShutdown() {
	// ... existing implementation
	p.PauseAll()

	// Wait for any downloads in "Pausing" state to finish transitioning
	// This ensures we don't exit while a database write is pending/active
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		p.mu.RLock()
		stillPausing := false
		for _, ad := range p.downloads {
			if ad.config.State != nil && ad.config.State.IsPausing() {
				stillPausing = true
				break
			}
		}
		p.mu.RUnlock()

		if !stillPausing {
			break
		}

		select {
		case <-ctx.Done():
			utils.Debug("GracefulShutdown: timed out waiting for downloads to pause")
			return // Return from function, loop will exit
		case <-ticker.C:
			continue
		}
	}

	p.wg.Wait() // Blocks until all workers call Done()
}

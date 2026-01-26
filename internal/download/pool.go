package download

import (
	"context"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/surge-downloader/surge/internal/download/types"
	"github.com/surge-downloader/surge/internal/messages"
)

// activeDownload tracks a download that's currently running
type activeDownload struct {
	config types.DownloadConfig
	cancel context.CancelFunc
}

type WorkerPool struct {
	taskChan     chan types.DownloadConfig
	progressCh   chan<- tea.Msg
	downloads    map[string]*activeDownload // Track active downloads for pause/resume
	mu           sync.RWMutex
	wg           sync.WaitGroup //We use this to wait for all active downloads to pause before exiting the program
	maxDownloads int
}

func NewWorkerPool(progressCh chan<- tea.Msg, maxDownloads int) *WorkerPool {
	if maxDownloads < 1 {
		maxDownloads = 3 // Default to 3 if invalid
	}
	pool := &WorkerPool{
		taskChan:     make(chan types.DownloadConfig, 100), //We make it buffered to avoid blocking add
		progressCh:   progressCh,
		downloads:    make(map[string]*activeDownload),
		maxDownloads: maxDownloads,
	}
	for i := 0; i < maxDownloads; i++ {
		go pool.worker()
	}
	return pool
}

func (p *WorkerPool) Add(cfg types.DownloadConfig) {
	p.taskChan <- cfg
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
		ad.config.State.Pause()
	}

	// Send pause message
	if p.progressCh != nil {
		downloaded := int64(0)
		if ad.config.State != nil {
			downloaded = ad.config.State.Downloaded.Load()
		}
		p.progressCh <- messages.DownloadPausedMsg{
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
		// Only pause downloads that are actually active (not already paused or done)
		if ad != nil && ad.config.State != nil && !ad.config.State.IsPaused() && !ad.config.State.Done.Load() {
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

	// Clear paused flag
	if ad.config.State != nil {
		ad.config.State.Resume()
	}

	// Re-queue the download
	p.Add(ad.config)

	// Send resume message
	if p.progressCh != nil {
		p.progressCh <- messages.DownloadResumedMsg{
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
		p.downloads[cfg.ID] = ad
		p.mu.Unlock()

		err := TUIDownload(ctx, cfg)

		// Check if this was a pause (not an error)
		isPaused := cfg.State != nil && cfg.State.IsPaused()

		if err != nil && !isPaused {
			if cfg.State != nil {
				cfg.State.SetError(err)
			}
			if p.progressCh != nil {
				p.progressCh <- messages.DownloadErrorMsg{DownloadID: cfg.ID, Err: err}
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
	p.mu.RUnlock()

	if !exists || ad == nil {
		return nil
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

	if state.IsPaused() {
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

	// Calculate speed
	downloaded, _, elapsed, _, sessionStart := state.GetProgress()
	sessionDownloaded := downloaded - sessionStart
	if elapsed.Seconds() > 0 && sessionDownloaded > 0 {
		status.Speed = float64(sessionDownloaded) / elapsed.Seconds()
	}

	return status
}

// GracefulShutdown pauses all downloads and waits for them to save state
func (p *WorkerPool) GracefulShutdown() {
	// ... existing implementation
	p.PauseAll()
	p.wg.Wait() // Blocks until all workers call Done()
}

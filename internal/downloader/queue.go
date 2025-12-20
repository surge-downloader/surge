package downloader

import (
	"context"
	"surge/internal/messages"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const maxDownloads = 3 //We limit the max no of downloads to 3 at a time(XDM does this)

type WorkerPool struct {
	taskChan   chan DownloadConfig
	progressCh chan<- tea.Msg
}

func NewWorkerPool(progressCh chan<- tea.Msg) *WorkerPool {
	pool := &WorkerPool{
		taskChan:   make(chan DownloadConfig, 100), //We make it buffered to avoid blocking add
		progressCh: progressCh,
	}
	for i := 0; i < maxDownloads; i++ {
		go pool.worker()
	}
	return pool
}

func (p *WorkerPool) Add(cfg DownloadConfig) {
	p.taskChan <- cfg
}

func (p *WorkerPool) worker() {
	for cfg := range p.taskChan {
		err := TUIDownload(context.Background(), cfg)
		if err != nil {
			if cfg.State != nil {
				cfg.State.SetError(err)
			}
			if p.progressCh != nil {
				p.progressCh <- messages.DownloadErrorMsg{DownloadID: cfg.ID, Err: err}
			}
		} else {
			if cfg.State != nil {
				cfg.State.Done.Store(true)

			}
			if p.progressCh != nil {
				p.progressCh <- messages.DownloadCompleteMsg{DownloadID: cfg.ID, Elapsed: time.Since(cfg.State.StartTime), Total: cfg.State.TotalSize}
			}
		}
	}
}

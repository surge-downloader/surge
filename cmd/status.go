package cmd

import (
	"sync"
	"time"
)

// DownloadStatus represents the state of a download
type DownloadStatus struct {
	ID          string  `json:"id"`
	URL         string  `json:"url"`
	Filename    string  `json:"filename"`
	TotalSize   int64   `json:"total_size"`
	Downloaded  int64   `json:"downloaded"`
	Progress    float64 `json:"progress"` // Percentage 0-100
	Speed       float64 `json:"speed"`    // MB/s
	Status      string  `json:"status"`   // "queued", "downloading", "completed", "error"
	Error       string  `json:"error,omitempty"`
	StartedAt   time.Time
	CompletedAt time.Time
}

// downloadRegistry tracks active and recently completed downloads
type downloadRegistry struct {
	mu        sync.RWMutex
	downloads map[string]*DownloadStatus
}

var registry = &downloadRegistry{
	downloads: make(map[string]*DownloadStatus),
}

func (r *downloadRegistry) Add(id, url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.downloads[id] = &DownloadStatus{
		ID:        id,
		URL:       url,
		Status:    "queued",
		StartedAt: time.Now(),
	}
}

func (r *downloadRegistry) Update(id string, update func(*DownloadStatus)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if status, exists := r.downloads[id]; exists {
		update(status)
	}
}

func (r *downloadRegistry) Get(id string) (*DownloadStatus, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	status, exists := r.downloads[id]
	if !exists {
		return nil, false
	}
	// Return a copy to avoid race conditions if caller modifies it (though returning pointer is risky, for read-only JSON marshaling it's mostly fine if we are careful, but copy is safer)
	copy := *status
	return &copy, true
}

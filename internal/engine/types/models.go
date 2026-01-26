package types

// Task represents a byte range to download
type Task struct {
	Offset int64 `json:"offset"`
	Length int64 `json:"length"`
}

// DownloadState represents persisted download state for resume
type DownloadState struct {
	ID         string `json:"id"`       // Unique ID of the download
	URLHash    string `json:"url_hash"` // Hash of URL only (for master list compatibility)
	URL        string `json:"url"`
	DestPath   string `json:"dest_path"`
	TotalSize  int64  `json:"total_size"`
	Downloaded int64  `json:"downloaded"`
	Tasks      []Task `json:"tasks"` // Remaining tasks
	Filename   string `json:"filename"`
	CreatedAt  int64  `json:"created_at"` // Unix timestamp
	PausedAt   int64  `json:"paused_at"`  // Unix timestamp
}

// DownloadEntry represents a download in the master list
type DownloadEntry struct {
	ID          string `json:"id"`       // Unique ID of the download
	URLHash     string `json:"url_hash"` // Hash of URL only (backward compatibility)
	URL         string `json:"url"`
	DestPath    string `json:"dest_path"`
	Filename    string `json:"filename"`
	Status      string `json:"status"`       // "paused", "completed", "error"
	TotalSize   int64  `json:"total_size"`   // File size in bytes
	Downloaded  int64  `json:"downloaded"`   // Bytes downloaded
	CompletedAt int64  `json:"completed_at"` // Unix timestamp when completed
	TimeTaken   int64  `json:"time_taken"`   // Duration in milliseconds (for completed)
}

// MasterList holds all tracked downloads
type MasterList struct {
	Downloads []DownloadEntry `json:"downloads"`
}

// DownloadStatus represents the transient status of an active download
type DownloadStatus struct {
	ID         string  `json:"id"`
	URL        string  `json:"url"`
	Filename   string  `json:"filename"`
	TotalSize  int64   `json:"total_size"`
	Downloaded int64   `json:"downloaded"`
	Progress   float64 `json:"progress"` // Percentage 0-100
	Speed      float64 `json:"speed"`    // MB/s
	Status     string  `json:"status"`   // "queued", "paused", "downloading", "completed", "error"
	Error      string  `json:"error,omitempty"`
}

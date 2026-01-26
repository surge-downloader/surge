package events

import (
	"time"
)

// ProgressMsg represents a progress update from the downloader
type ProgressMsg struct {
	DownloadID        string
	Downloaded        int64
	Total             int64
	Speed             float64 // bytes per second
	ActiveConnections int
}

// DownloadCompleteMsg signals that the download finished successfully
type DownloadCompleteMsg struct {
	DownloadID string
	Filename   string
	Elapsed    time.Duration
	Total      int64
}

// DownloadErrorMsg signals that an error occurred
type DownloadErrorMsg struct {
	DownloadID string
	Err        error
}

// DownloadStartedMsg is sent when a download actually starts (after metadata fetch)
type DownloadStartedMsg struct {
	DownloadID string
	URL        string
	Filename   string
	Total      int64
	DestPath   string // Full path to the destination file
}

type DownloadPausedMsg struct {
	DownloadID string
	Downloaded int64
}

type DownloadResumedMsg struct {
	DownloadID string
}

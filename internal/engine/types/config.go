package types

import (
	"time"
)

// Size constants
const (
	KB = 1024
	MB = 1024 * KB
	GB = 1024 * MB

	// Megabyte as float for display calculations
	Megabyte = 1024.0 * 1024.0

	// IncompleteSuffix is appended to files while downloading
	IncompleteSuffix = ".surge"
)

// Chunk size constants for concurrent downloads
const (
	MinChunk     = 2 * MB  // Minimum chunk size
	MaxChunk     = 16 * MB // Maximum chunk size
	TargetChunk  = 8 * MB  // Target chunk size
	AlignSize    = 4 * KB  // Align chunks to 4KB for filesystem
	WorkerBuffer = 512 * KB

	TasksPerWorker = 4 // Target tasks per connection
)

// Connection limits
const (
	PerHostMax = 64 // Max concurrent connections per host
)

// HTTP Client Tuning
const (
	DefaultMaxIdleConns          = 100
	DefaultIdleConnTimeout       = 90 * time.Second
	DefaultTLSHandshakeTimeout   = 10 * time.Second
	DefaultResponseHeaderTimeout = 15 * time.Second
	DefaultExpectContinueTimeout = 1 * time.Second
	DialTimeout                  = 10 * time.Second
	KeepAliveDuration            = 30 * time.Second
	ProbeTimeout                 = 30 * time.Second
)

// Channel buffer sizes
const (
	ProgressChannelBuffer = 100
)

// DownloadConfig contains all parameters needed to start a download
type DownloadConfig struct {
	URL        string
	OutputPath string
	DestPath   string // Full destination path (for resume state lookup)
	ID         string
	Filename   string
	Verbose    bool
	IsResume   bool // True if this is explicitly a resume, not a fresh download
	ProgressCh chan<- any
	State      *ProgressState
	Runtime    *RuntimeConfig // Dynamic settings from user config
}

// RuntimeConfig holds dynamic settings that can override defaults
type RuntimeConfig struct {
	MaxConnectionsPerHost int
	MaxGlobalConnections  int
	UserAgent             string
	MinChunkSize          int64
	MaxChunkSize          int64
	TargetChunkSize       int64
	WorkerBufferSize      int
	MaxTaskRetries        int
	SlowWorkerThreshold   float64
	SlowWorkerGracePeriod time.Duration
	StallTimeout          time.Duration
	SpeedEmaAlpha         float64
}

// GetUserAgent returns the configured user agent or the default
func (r *RuntimeConfig) GetUserAgent() string {
	if r == nil || r.UserAgent == "" {
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	}
	return r.UserAgent
}

// GetMaxConnectionsPerHost returns configured value or default
func (r *RuntimeConfig) GetMaxConnectionsPerHost() int {
	if r == nil || r.MaxConnectionsPerHost <= 0 {
		return PerHostMax
	}
	return r.MaxConnectionsPerHost
}

// GetMinChunkSize returns configured value or default
func (r *RuntimeConfig) GetMinChunkSize() int64 {
	if r == nil || r.MinChunkSize <= 0 {
		return MinChunk
	}
	return r.MinChunkSize
}

// GetMaxChunkSize returns configured value or default
func (r *RuntimeConfig) GetMaxChunkSize() int64 {
	if r == nil || r.MaxChunkSize <= 0 {
		return MaxChunk
	}
	return r.MaxChunkSize
}

// GetTargetChunkSize returns configured value or default
func (r *RuntimeConfig) GetTargetChunkSize() int64 {
	if r == nil || r.TargetChunkSize <= 0 {
		return TargetChunk
	}
	return r.TargetChunkSize
}

// GetWorkerBufferSize returns configured value or default
func (r *RuntimeConfig) GetWorkerBufferSize() int {
	if r == nil || r.WorkerBufferSize <= 0 {
		return WorkerBuffer
	}
	return r.WorkerBufferSize
}

const (
	MaxTaskRetries = 3
	RetryBaseDelay = 200 * time.Millisecond

	// Health check constants
	HealthCheckInterval = 1 * time.Second // How often to check worker health
	SlowWorkerThreshold = 0.50            // Restart if speed < x times of mean
	SlowWorkerGrace     = 5 * time.Second // Grace period before checking speed
	StallTimeout        = 5 * time.Second // Restart if no data for x seconds
	SpeedEMAAlpha       = 0.3             // EMA smoothing factor
	MinAbsoluteSpeed    = 100 * KB        // Don't cancel workers above this speed
)

// GetMaxTaskRetries returns configured value or default
func (r *RuntimeConfig) GetMaxTaskRetries() int {
	if r == nil || r.MaxTaskRetries <= 0 {
		return MaxTaskRetries
	}
	return r.MaxTaskRetries
}

// GetSlowWorkerThreshold returns configured value or default
func (r *RuntimeConfig) GetSlowWorkerThreshold() float64 {
	if r == nil || r.SlowWorkerThreshold <= 0 {
		return SlowWorkerThreshold
	}
	return r.SlowWorkerThreshold
}

// GetSlowWorkerGracePeriod returns configured value or default
func (r *RuntimeConfig) GetSlowWorkerGracePeriod() time.Duration {
	if r == nil || r.SlowWorkerGracePeriod <= 0 {
		return SlowWorkerGrace
	}
	return r.SlowWorkerGracePeriod
}

// GetStallTimeout returns configured value or default
func (r *RuntimeConfig) GetStallTimeout() time.Duration {
	if r == nil || r.StallTimeout <= 0 {
		return StallTimeout
	}
	return r.StallTimeout
}

// GetSpeedEmaAlpha returns configured value or default
func (r *RuntimeConfig) GetSpeedEmaAlpha() float64 {
	if r == nil || r.SpeedEmaAlpha <= 0 {
		return SpeedEMAAlpha
	}
	return r.SpeedEmaAlpha
}

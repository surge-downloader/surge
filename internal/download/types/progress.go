package types

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

type ProgressState struct {
	ID            string
	Downloaded    atomic.Int64
	TotalSize     int64
	StartTime     time.Time
	ActiveWorkers atomic.Int32
	Done          atomic.Bool
	Error         atomic.Pointer[error]
	Paused        atomic.Bool
	CancelFunc    context.CancelFunc

	SessionStartBytes int64      // SessionStartBytes tracks how many bytes were already downloaded when the current session started
	RateLimitedUntil  time.Time  // When rate limiting expires (zero if not limited)
	mu                sync.Mutex // Protects TotalSize, StartTime, SessionStartBytes, RateLimitedUntil
}

func NewProgressState(id string, totalSize int64) *ProgressState {
	return &ProgressState{
		ID:        id,
		TotalSize: totalSize,
		StartTime: time.Now(),
	}
}

func (ps *ProgressState) SetTotalSize(size int64) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.TotalSize = size
	ps.SessionStartBytes = ps.Downloaded.Load()
	ps.StartTime = time.Now()
}

func (ps *ProgressState) SetError(err error) {
	ps.Error.Store(&err)
}

func (ps *ProgressState) GetError() error {
	if e := ps.Error.Load(); e != nil {
		return *e
	}
	return nil
}

func (ps *ProgressState) GetProgress() (downloaded int64, total int64, elapsed time.Duration, connections int32, sessionStartBytes int64) {
	downloaded = ps.Downloaded.Load()
	connections = ps.ActiveWorkers.Load()

	ps.mu.Lock()
	total = ps.TotalSize
	elapsed = time.Since(ps.StartTime)
	sessionStartBytes = ps.SessionStartBytes
	ps.mu.Unlock()
	return
}

func (ps *ProgressState) Pause() {
	ps.Paused.Store(true)
	if ps.CancelFunc != nil {
		ps.CancelFunc()
	}
}

func (ps *ProgressState) Resume() {
	ps.Paused.Store(false)
}

func (ps *ProgressState) IsPaused() bool {
	return ps.Paused.Load()
}

// SetRateLimited sets the rate limit expiry time for UI feedback
func (ps *ProgressState) SetRateLimited(until time.Time) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.RateLimitedUntil = until
}

// GetRateLimitedUntil returns when rate limiting expires.
// Returns zero time if not rate limited.
func (ps *ProgressState) GetRateLimitedUntil() time.Time {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.RateLimitedUntil
}

// IsRateLimited returns true if currently rate limited
func (ps *ProgressState) IsRateLimited() bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return !ps.RateLimitedUntil.IsZero() && time.Now().Before(ps.RateLimitedUntil)
}

// ClearRateLimit clears the rate limit status
func (ps *ProgressState) ClearRateLimit() {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.RateLimitedUntil = time.Time{}
}

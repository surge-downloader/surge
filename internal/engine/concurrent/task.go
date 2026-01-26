package concurrent

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/surge-downloader/surge/internal/engine/types"
)

// ActiveTask tracks a task currently being processed by a worker
type ActiveTask struct {
	Task          types.Task
	CurrentOffset int64 // Atomic
	StopAt        int64 // Atomic

	// Health monitoring fields
	LastActivity int64              // Atomic: Unix nano timestamp of last data received
	Speed        float64            // EMA-smoothed speed in bytes/sec (protected by mutex)
	StartTime    time.Time          // When this task started
	Cancel       context.CancelFunc // Cancel function to abort this task
	SpeedMu      sync.Mutex         // Protects Speed field

	// Sliding window for recent speed tracking
	WindowStart time.Time // When current measurement window started
	WindowBytes int64     // Bytes downloaded in current window (atomic)
}

// RemainingBytes returns the number of bytes left for this task
func (at *ActiveTask) RemainingBytes() int64 {
	current := atomic.LoadInt64(&at.CurrentOffset)
	stopAt := atomic.LoadInt64(&at.StopAt)
	if current >= stopAt {
		return 0
	}
	return stopAt - current
}

// RemainingTask returns a Task representing the remaining work, or nil if complete
func (at *ActiveTask) RemainingTask() *types.Task {
	current := atomic.LoadInt64(&at.CurrentOffset)
	stopAt := atomic.LoadInt64(&at.StopAt)
	if current >= stopAt {
		return nil
	}
	return &types.Task{Offset: current, Length: stopAt - current}
}

// GetSpeed returns the current EMA-smoothed speed
func (at *ActiveTask) GetSpeed() float64 {
	at.SpeedMu.Lock()
	defer at.SpeedMu.Unlock()
	return at.Speed
}

// alignedSplitSize calculates a split size that is half of remaining, aligned to AlignSize
// Returns 0 if the split would be smaller than MinChunk
func alignedSplitSize(remaining int64) int64 {
	half := (remaining / 2 / types.AlignSize) * types.AlignSize
	if half < types.MinChunk {
		return 0
	}
	return half
}

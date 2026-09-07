package progress

import "sync/atomic"

// ByteTracker handles thread-safe lock-free byte counting.
// Cache line padding (64 bytes) isolates the high-frequency write counter
// (Downloaded) from read-mostly counters (VerifiedProgress, TotalSize), eliminating
// multicore false sharing / MESI cache line bouncing during batch flushes.
type ByteTracker struct {
	Downloaded atomic.Int64
	_          [56]byte // pad Downloaded (8B) to its own 64B cache line

	VerifiedProgress atomic.Int64
	TotalSize        atomic.Int64 // Updated dynamically if size is discovered during download
	_                [48]byte     // pad read counters (16B) to the next 64B cache line
}

// SetTotalSize initializes the total size.
func (b *ByteTracker) SetTotalSize(size int64) {
	b.TotalSize.Store(size)
}

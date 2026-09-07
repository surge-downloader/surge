package progress

import (
	"sync"
	"sync/atomic"

	"github.com/SurgeDM/Surge/internal/types"
	"github.com/SurgeDM/Surge/internal/utils"
)

type BitmapTracker struct {
	mu              sync.RWMutex
	chunkStatus     []atomic.Int32
	chunkProgress   []atomic.Int64
	actualChunkSize int64
	width           int
}

// bitmapLayout returns the number of tracked chunks and backing bytes for a
// 2-bit-per-chunk bitmap.
func bitmapLayout(totalSize, chunkSize int64) (numChunks int, bytesNeeded int, ok bool) {
	if totalSize <= 0 || chunkSize <= 0 {
		return 0, 0, false
	}

	numChunks = int((totalSize + chunkSize - 1) / chunkSize)
	if numChunks <= 0 {
		return 0, 0, false
	}

	bytesNeeded = (numChunks + 3) / 4
	return numChunks, bytesNeeded, true
}

// Reset clears the bitmap completely.
func (b *BitmapTracker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.width > 0 {
		b.chunkStatus = make([]atomic.Int32, b.width)
		b.chunkProgress = make([]atomic.Int64, b.width)
	}
}

// InitBitmap initializes the chunk bitmap.
func (b *BitmapTracker) InitBitmap(totalSize int64, chunkSize int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.chunkStatus) > 0 && b.actualChunkSize == chunkSize {
		// Already initialized and the chunk size is correct.
		// NOTE: TotalSize check is left to the caller or implicitly valid.
		return
	}

	utils.Debug("InitBitmap: Total=%d, ChunkSize=%d", totalSize, chunkSize)
	if chunkSize <= 0 {
		return
	}

	numChunks, _, ok := bitmapLayout(totalSize, chunkSize)
	if !ok {
		return
	}

	b.actualChunkSize = chunkSize
	b.width = numChunks
	b.chunkStatus = make([]atomic.Int32, numChunks)
	b.chunkProgress = make([]atomic.Int64, numChunks)
}

// RestoreBitmap restores the chunk bitmap from saved state.
func (b *BitmapTracker) RestoreBitmap(totalSize int64, bitmap []byte, actualChunkSize int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(bitmap) == 0 || actualChunkSize <= 0 || totalSize <= 0 {
		return
	}

	numChunks, _, ok := bitmapLayout(totalSize, actualChunkSize)
	if !ok {
		return
	}

	b.actualChunkSize = actualChunkSize
	b.width = numChunks
	b.chunkStatus = make([]atomic.Int32, numChunks)

	// Unpack from byte slice to chunkStatus array
	for i := 0; i < numChunks; i++ {
		byteIndex := i / 4
		if byteIndex < len(bitmap) {
			bitOffset := (i % 4) * 2
			val := (bitmap[byteIndex] >> bitOffset) & 3
			b.chunkStatus[i].Store(int32(val))
		}
	}

	if len(b.chunkProgress) != numChunks {
		b.chunkProgress = make([]atomic.Int64, numChunks)
	}
}

// SetChunkProgress updates chunk progress array from external sources.
func (b *BitmapTracker) SetChunkProgress(progress []int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(progress) == 0 {
		return
	}
	if len(b.chunkProgress) != len(progress) {
		b.chunkProgress = make([]atomic.Int64, len(progress))
	}
	for i, v := range progress {
		b.chunkProgress[i].Store(v)
	}
}

// SetChunkState sets the state for a specific chunk index.
func (b *BitmapTracker) SetChunkState(index int, status types.ChunkStatus) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	b.setChunkState(index, status)
}

// setChunkState sets the state (internal, lock-free on the atomic).
func (b *BitmapTracker) setChunkState(index int, status types.ChunkStatus) {
	if index < 0 || index >= b.width {
		return
	}
	b.chunkStatus[index].Store(int32(status))
}

// GetChunkState gets the state for a specific chunk index.
func (b *BitmapTracker) GetChunkState(index int) types.ChunkStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.getChunkState(index)
}

func (b *BitmapTracker) getChunkState(index int) types.ChunkStatus {
	if index < 0 || index >= b.width {
		return types.ChunkPending
	}
	return types.ChunkStatus(b.chunkStatus[index].Load())
}

// UpdateChunkStatus updates the bitmap based on byte range and returns the incremented progress.
func (b *BitmapTracker) UpdateChunkStatus(totalSize, offset, length int64, status types.ChunkStatus) (increment int64) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.actualChunkSize == 0 || len(b.chunkStatus) == 0 {
		return 0
	}

	startIdx := int(offset / b.actualChunkSize)
	endIdx := int((offset + length - 1) / b.actualChunkSize)

	if startIdx < 0 {
		startIdx = 0
	}
	if endIdx >= b.width {
		endIdx = b.width - 1
	}

	var totalIncrement int64

	for i := startIdx; i <= endIdx; i++ {
		chunkStart := int64(i) * b.actualChunkSize
		chunkEnd := chunkStart + b.actualChunkSize
		if chunkEnd > totalSize {
			chunkEnd = totalSize
		}

		updateStart := offset
		if updateStart < chunkStart {
			updateStart = chunkStart
		}

		updateEnd := offset + length
		if updateEnd > chunkEnd {
			updateEnd = chunkEnd
		}

		overlap := updateEnd - updateStart
		if overlap < 0 {
			overlap = 0
		}

		switch status {
		case types.ChunkCompleted:
			// Lock-free CAS loop to avoid overcounting under concurrent updates
			for {
				currentProg := b.chunkProgress[i].Load()
				remainingSpace := (chunkEnd - chunkStart) - currentProg

				inc := overlap
				if inc > remainingSpace {
					inc = remainingSpace
				}

				if inc <= 0 {
					// We might have already reached the end or overlap is zero.
					if currentProg >= (chunkEnd - chunkStart) {
						b.chunkStatus[i].Store(int32(types.ChunkCompleted))
					}
					break
				}

				if b.chunkProgress[i].CompareAndSwap(currentProg, currentProg+inc) {
					totalIncrement += inc
					if currentProg+inc >= (chunkEnd - chunkStart) {
						b.chunkStatus[i].Store(int32(types.ChunkCompleted))
					} else {
						b.chunkStatus[i].CompareAndSwap(int32(types.ChunkPending), int32(types.ChunkDownloading))
					}
					break
				}
			}
		case types.ChunkDownloading:
			b.chunkStatus[i].CompareAndSwap(int32(types.ChunkPending), int32(types.ChunkDownloading))
		}
	}

	return totalIncrement
}

// RecalculateProgress reconstructs ChunkProgress from remaining tasks and returns total verified bytes.
func (b *BitmapTracker) RecalculateProgress(totalSize int64, remainingTasks []types.Task) (totalVerified int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.actualChunkSize == 0 || b.width == 0 {
		return 0
	}

	b.chunkProgress = make([]atomic.Int64, b.width)
	var total int64
	for i := 0; i < b.width; i++ {
		chunkStart := int64(i) * b.actualChunkSize
		chunkEnd := chunkStart + b.actualChunkSize
		if chunkEnd > totalSize {
			chunkEnd = totalSize
		}
		prog := chunkEnd - chunkStart
		b.chunkProgress[i].Store(prog)
		total += prog
	}

	for _, task := range remainingTasks {
		offset := task.Offset
		length := task.Length

		startIdx := int(offset / b.actualChunkSize)
		endIdx := int((offset + length - 1) / b.actualChunkSize)

		if startIdx < 0 {
			startIdx = 0
		}
		if endIdx >= b.width {
			endIdx = b.width - 1
		}

		for i := startIdx; i <= endIdx; i++ {
			chunkStart := int64(i) * b.actualChunkSize
			chunkEnd := chunkStart + b.actualChunkSize
			if chunkEnd > totalSize {
				chunkEnd = totalSize
			}

			taskStart := offset
			if taskStart < chunkStart {
				taskStart = chunkStart
			}

			taskEnd := offset + length
			if taskEnd > chunkEnd {
				taskEnd = chunkEnd
			}

			overlap := taskEnd - taskStart
			if overlap > 0 {
				newProg := b.chunkProgress[i].Add(-overlap)
				total -= overlap

				if newProg < 0 {
					total += -newProg
					b.chunkProgress[i].Store(0)
				}
			}
		}
	}

	// Restore-time ChunkCompleted bits mean the bytes are already on disk.
	// Remaining coverage must not un-complete them before the status rewrite.
	for i := 0; i < b.width; i++ {
		if types.ChunkStatus(b.chunkStatus[i].Load()) != types.ChunkCompleted {
			continue
		}
		chunkStart := int64(i) * b.actualChunkSize
		chunkEnd := chunkStart + b.actualChunkSize
		if chunkEnd > totalSize {
			chunkEnd = totalSize
		}
		chunkSize := chunkEnd - chunkStart
		current := b.chunkProgress[i].Load()
		if current < chunkSize {
			total += chunkSize - current
			b.chunkProgress[i].Store(chunkSize)
		}
	}

	for i := 0; i < b.width; i++ {
		chunkStart := int64(i) * b.actualChunkSize
		chunkEnd := chunkStart + b.actualChunkSize
		if chunkEnd > totalSize {
			chunkEnd = totalSize
		}
		chunkSize := chunkEnd - chunkStart

		prog := b.chunkProgress[i].Load()
		if prog >= chunkSize {
			b.chunkProgress[i].Store(chunkSize)
			b.chunkStatus[i].Store(int32(types.ChunkCompleted))
		} else if prog > 0 {
			b.chunkStatus[i].Store(int32(types.ChunkDownloading))
		} else {
			b.chunkProgress[i].Store(0)
			b.chunkStatus[i].Store(int32(types.ChunkPending))
		}
	}
	return total
}

// GetBitmapSnapshot returns a copy of bitmap metadata and optionally chunk progress.
func (b *BitmapTracker) GetBitmapSnapshot(totalSize int64, includeProgress bool) ([]byte, int, int64, int64, []int64) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if len(b.chunkStatus) == 0 {
		return nil, 0, 0, 0, nil
	}

	_, bytesNeeded, _ := bitmapLayout(totalSize, b.actualChunkSize)
	result := make([]byte, bytesNeeded)

	for i := 0; i < b.width; i++ {
		status := b.chunkStatus[i].Load()
		byteIndex := i / 4
		bitOffset := (i % 4) * 2
		val := byte(status) << bitOffset
		result[byteIndex] |= val
	}

	var progressResult []int64
	if includeProgress {
		progressResult = make([]int64, len(b.chunkProgress))
		for i := 0; i < len(b.chunkProgress); i++ {
			progressResult[i] = b.chunkProgress[i].Load()
		}
	}

	return result, b.width, totalSize, b.actualChunkSize, progressResult
}

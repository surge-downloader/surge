package progress

import (
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/SurgeDM/Surge/internal/types"
)

func TestBitmapTracker_InitAndSnapshot(t *testing.T) {
	bt := &BitmapTracker{}
	totalSize := int64(1024 * 1024)
	chunkSize := int64(256 * 1024)

	bt.InitBitmap(totalSize, chunkSize)

	bitmap, width, ts, acs, prog := bt.GetBitmapSnapshot(totalSize, true)

	if width != 4 {
		t.Errorf("expected width 4, got %d", width)
	}
	if ts != totalSize {
		t.Errorf("expected totalSize %d, got %d", totalSize, ts)
	}
	if acs != chunkSize {
		t.Errorf("expected actualChunkSize %d, got %d", chunkSize, acs)
	}
	if len(bitmap) != 1 {
		t.Errorf("expected bitmap length 1, got %d", len(bitmap))
	}
	if len(prog) != 4 {
		t.Errorf("expected prog length 4, got %d", len(prog))
	}
}

func TestBitmapTracker_UpdateChunkStatus(t *testing.T) {
	bt := &BitmapTracker{}
	totalSize := int64(1000)
	chunkSize := int64(250)
	bt.InitBitmap(totalSize, chunkSize) // 4 chunks

	// Update first half of chunk 0
	inc := bt.UpdateChunkStatus(totalSize, 0, 125, types.ChunkCompleted)
	if inc != 125 {
		t.Errorf("expected increment 125, got %d", inc)
	}

	state := bt.GetChunkState(0)
	if state != types.ChunkDownloading {
		t.Errorf("expected state ChunkDownloading, got %v", state)
	}

	// Update second half of chunk 0
	inc = bt.UpdateChunkStatus(totalSize, 125, 125, types.ChunkCompleted)
	if inc != 125 {
		t.Errorf("expected increment 125, got %d", inc)
	}

	state = bt.GetChunkState(0)
	if state != types.ChunkCompleted {
		t.Errorf("expected state ChunkCompleted, got %v", state)
	}

	// Try to update chunk 0 again, should return 0 increment
	inc = bt.UpdateChunkStatus(totalSize, 0, 250, types.ChunkCompleted)
	if inc != 0 {
		t.Errorf("expected increment 0, got %d", inc)
	}
}

func TestBitmapTracker_ConcurrentUpdates(t *testing.T) {
	bt := &BitmapTracker{}
	totalSize := int64(10 * 1024 * 1024) // 10 MB
	chunkSize := int64(1024 * 1024)      // 1 MB
	bt.InitBitmap(totalSize, chunkSize)  // 10 chunks

	var wg sync.WaitGroup
	// Simulate 100 workers hammering the same chunks
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)))
			for j := 0; j < 1000; j++ {
				offset := rng.Int63n(totalSize)
				length := int64(1024) // 1 KB writes
				bt.UpdateChunkStatus(totalSize, offset, length, types.ChunkCompleted)
			}
		}(i)
	}
	wg.Wait()

	// Ensure we didn't panic and that the max bytes per chunk are strictly enforced
	_, _, _, _, prog := bt.GetBitmapSnapshot(totalSize, true)
	for i, p := range prog {
		if p > chunkSize {
			t.Errorf("chunk %d has progress %d exceeding chunk size %d", i, p, chunkSize)
		}
	}
}

func TestBitmapTracker_RecalculateProgress(t *testing.T) {
	bt := &BitmapTracker{}
	totalSize := int64(1000)
	chunkSize := int64(250)
	bt.InitBitmap(totalSize, chunkSize)

	// remaining tasks
	tasks := []types.Task{
		{Offset: 0, Length: 250},   // Chunk 0 completely missing
		{Offset: 250, Length: 125}, // Chunk 1 half missing
	}

	totalVerified := bt.RecalculateProgress(totalSize, tasks)
	// total = 1000, missing = 375, verified should be 625
	if totalVerified != 625 {
		t.Errorf("expected totalVerified 625, got %d", totalVerified)
	}

	if bt.GetChunkState(0) != types.ChunkPending {
		t.Errorf("chunk 0 should be pending")
	}
	if bt.GetChunkState(1) != types.ChunkDownloading {
		t.Errorf("chunk 1 should be downloading")
	}
	if bt.GetChunkState(2) != types.ChunkCompleted {
		t.Errorf("chunk 2 should be completed")
	}
	if bt.GetChunkState(3) != types.ChunkCompleted {
		t.Errorf("chunk 3 should be completed")
	}
}

func TestBitmapTracker_RecalculateProgress_RestoredCompletedStaysFull(t *testing.T) {
	bt := &BitmapTracker{}
	totalSize := int64(1000)
	chunkSize := int64(250)
	bt.InitBitmap(totalSize, chunkSize)

	// Restore marked chunk 1 complete; remaining still covers it and half of chunk 2.
	bt.SetChunkState(1, types.ChunkCompleted)
	tasks := []types.Task{
		{Offset: 250, Length: 250}, // chunk 1 fully covered
		{Offset: 500, Length: 125}, // chunk 2 half remaining
	}

	totalVerified := bt.RecalculateProgress(totalSize, tasks)
	// full-minus-remaining would be 1000-375=625; restoring chunk 1 adds 250 → 875.
	if totalVerified != 875 {
		t.Errorf("expected totalVerified 875, got %d", totalVerified)
	}

	if bt.GetChunkState(0) != types.ChunkCompleted {
		t.Errorf("chunk 0 should stay completed (no remaining)")
	}
	if bt.GetChunkState(1) != types.ChunkCompleted {
		t.Errorf("chunk 1 restored Completed must stay Completed")
	}
	if bt.GetChunkState(2) != types.ChunkDownloading {
		t.Errorf("chunk 2 partial must not be starved")
	}
	if bt.GetChunkState(3) != types.ChunkCompleted {
		t.Errorf("chunk 3 should stay completed")
	}

	_, _, _, _, prog := bt.GetBitmapSnapshot(totalSize, true)
	if len(prog) != 4 {
		t.Fatalf("expected 4 chunk progress entries, got %d", len(prog))
	}
	if prog[1] != chunkSize {
		t.Errorf("chunk 1 progress = %d, want full %d", prog[1], chunkSize)
	}
	if prog[2] != 125 {
		t.Errorf("chunk 2 progress = %d, want 125 (partial not zeroed)", prog[2])
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

// Old implementation simulation for benchmarking
type OldBitmapTracker struct {
	mu              sync.Mutex
	bitmap          []byte
	chunkProgress   []int64
	actualChunkSize int64
	width           int
}

func (b *OldBitmapTracker) InitBitmap(totalSize int64, chunkSize int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	numChunks := int((totalSize + chunkSize - 1) / chunkSize)
	bytesNeeded := (numChunks + 3) / 4
	b.actualChunkSize = chunkSize
	b.width = numChunks
	b.bitmap = make([]byte, bytesNeeded)
	b.chunkProgress = make([]int64, numChunks)
}

func (b *OldBitmapTracker) UpdateChunkStatus(totalSize, offset, length int64, status types.ChunkStatus) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()

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

		if status == types.ChunkCompleted {
			inc := overlap
			remainingSpace := (chunkEnd - chunkStart) - b.chunkProgress[i]
			if inc > remainingSpace {
				inc = remainingSpace
			}

			if inc > 0 {
				b.chunkProgress[i] += inc
				totalIncrement += inc
			}
		}
	}
	return totalIncrement
}

func BenchmarkBitmap_OldMutexImplementation(b *testing.B) {
	totalSize := int64(100 * 1024 * 1024)
	chunkSize := int64(1024 * 1024)
	bt := &OldBitmapTracker{}
	bt.InitBitmap(totalSize, chunkSize)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var i int64
		for pb.Next() {
			offset := (i * 1024) % totalSize
			bt.UpdateChunkStatus(totalSize, offset, 1024, types.ChunkCompleted)
			i++
		}
	})
}

func BenchmarkBitmap_NewAtomicImplementation(b *testing.B) {
	totalSize := int64(100 * 1024 * 1024)
	chunkSize := int64(1024 * 1024)
	bt := &BitmapTracker{}
	bt.InitBitmap(totalSize, chunkSize)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var i int64
		for pb.Next() {
			offset := (i * 1024) % totalSize
			bt.UpdateChunkStatus(totalSize, offset, 1024, types.ChunkCompleted)
			i++
		}
	})
}

// To stress the snapshot rebuilding
func BenchmarkBitmap_NewAtomic_GetSnapshot(b *testing.B) {
	totalSize := int64(100 * 1024 * 1024)
	chunkSize := int64(1024 * 1024)
	bt := &BitmapTracker{}
	bt.InitBitmap(totalSize, chunkSize)

	// Simulate some downloaded state
	bt.UpdateChunkStatus(totalSize, 0, totalSize/2, types.ChunkCompleted)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bt.GetBitmapSnapshot(totalSize, true)
	}
}

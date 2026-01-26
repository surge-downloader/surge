package concurrent

import (
	"sync"
	"sync/atomic"

	"github.com/surge-downloader/surge/internal/engine/types"
)

// TaskQueue is a thread-safe work-stealing queue
type TaskQueue struct {
	tasks       []types.Task
	head        int
	mu          sync.Mutex
	cond        *sync.Cond
	done        bool
	idleWorkers int64 // Atomic counter for idle workers
}

func NewTaskQueue() *TaskQueue {
	tq := &TaskQueue{}
	tq.cond = sync.NewCond(&tq.mu)
	return tq
}

func (q *TaskQueue) Push(t types.Task) {
	q.mu.Lock()
	q.tasks = append(q.tasks, t)
	q.cond.Signal()
	q.mu.Unlock()
}

func (q *TaskQueue) PushMultiple(tasks []types.Task) {
	q.mu.Lock()
	q.tasks = append(q.tasks, tasks...)
	q.cond.Broadcast()
	q.mu.Unlock()
}

func (q *TaskQueue) Pop() (types.Task, bool) {
	// Mark as idle while waiting
	atomic.AddInt64(&q.idleWorkers, 1)

	q.mu.Lock()
	defer q.mu.Unlock()

	for len(q.tasks) == 0 && !q.done {
		q.cond.Wait()
	}

	// No longer idle once we have work (or are done)
	atomic.AddInt64(&q.idleWorkers, -1)

	if len(q.tasks) == 0 {
		return types.Task{}, false
	}

	t := q.tasks[q.head]
	q.head++
	if q.head > len(q.tasks)/2 {
		q.tasks = append([]types.Task(nil), q.tasks[q.head:]...)
		q.head = 0
	}
	return t, true
}

func (q *TaskQueue) Close() {
	q.mu.Lock()
	q.done = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

func (q *TaskQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.tasks) - q.head
}

func (q *TaskQueue) IdleWorkers() int64 {
	return atomic.LoadInt64(&q.idleWorkers)
}

// DrainRemaining returns all remaining tasks in the queue (used for pause/resume)
func (q *TaskQueue) DrainRemaining() []types.Task {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.head >= len(q.tasks) {
		return nil
	}

	remaining := make([]types.Task, len(q.tasks)-q.head)
	copy(remaining, q.tasks[q.head:])
	q.tasks = nil
	q.head = 0
	return remaining
}

// SplitLargestIfNeeded finds the largest queued task and splits it if > 2*MinChunk
// Returns true if a split occurred
func (q *TaskQueue) SplitLargestIfNeeded() bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Find largest queued task
	idx := -1
	var maxLen int64 = 0
	for i, t := range q.tasks {
		if t.Length > maxLen && t.Length > 2*types.MinChunk {
			maxLen = t.Length
			idx = i
		}
	}

	if idx == -1 {
		return false // No task large enough to split
	}

	t := q.tasks[idx]

	// Split in half, aligned to AlignSize
	half := alignedSplitSize(t.Length)
	if half == 0 {
		return false // Halves would be too small
	}

	left := types.Task{Offset: t.Offset, Length: half}
	right := types.Task{Offset: t.Offset + half, Length: t.Length - half}

	// Replace original with right half, append left half
	q.tasks[idx] = right
	q.tasks = append(q.tasks, left)

	// Wake up idle workers
	q.cond.Broadcast()
	return true
}

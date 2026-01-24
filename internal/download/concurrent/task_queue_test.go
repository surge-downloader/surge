package concurrent

import (
	"testing"

	"github.com/surge-downloader/surge/internal/download/types"
)

func TestNewTaskQueue(t *testing.T) {
	q := NewTaskQueue(types.MinChunk)

	if q == nil {
		t.Fatal("NewTaskQueue returned nil")
	}
	if q.Len() != 0 {
		t.Errorf("New queue length = %d, want 0", q.Len())
	}
	if q.IdleWorkers() != 0 {
		t.Errorf("New queue idle workers = %d, want 0", q.IdleWorkers())
	}
}

func TestTaskQueue_PushPop(t *testing.T) {
	q := NewTaskQueue(types.MinChunk)

	// Push a task
	task := types.Task{Offset: 0, Length: 1000}
	q.Push(task)

	if q.Len() != 1 {
		t.Errorf("Queue length after push = %d, want 1", q.Len())
	}

	// Pop in a goroutine (Pop blocks if empty)
	done := make(chan struct{})
	var popped types.Task
	var ok bool
	go func() {
		popped, ok = q.Pop()
		close(done)
	}()

	<-done

	if !ok {
		t.Error("Pop should return ok=true")
	}
	if popped.Offset != 0 || popped.Length != 1000 {
		t.Errorf("Popped task = {%d, %d}, want {0, 1000}", popped.Offset, popped.Length)
	}
}

func TestTaskQueue_PushMultiple(t *testing.T) {
	q := NewTaskQueue(types.MinChunk)

	tasks := []types.Task{
		{Offset: 0, Length: 1000},
		{Offset: 1000, Length: 1000},
		{Offset: 2000, Length: 1000},
	}
	q.PushMultiple(tasks)

	if q.Len() != 3 {
		t.Errorf("Queue length = %d, want 3", q.Len())
	}
}

func TestTaskQueue_Close(t *testing.T) {
	q := NewTaskQueue(types.MinChunk)

	// Start a goroutine waiting on Pop
	done := make(chan struct{})
	go func() {
		_, ok := q.Pop()
		if ok {
			t.Error("Pop should return ok=false after Close on empty queue")
		}
		close(done)
	}()

	// Close the queue
	q.Close()

	<-done
}

func TestTaskQueue_DrainRemaining(t *testing.T) {
	q := NewTaskQueue(types.MinChunk)

	tasks := []types.Task{
		{Offset: 0, Length: 1000},
		{Offset: 1000, Length: 1000},
		{Offset: 2000, Length: 1000},
	}
	q.PushMultiple(tasks)

	remaining := q.DrainRemaining()

	if len(remaining) != 3 {
		t.Errorf("Drained %d tasks, want 3", len(remaining))
	}
	if q.Len() != 0 {
		t.Errorf("Queue length after drain = %d, want 0", q.Len())
	}
}

func TestTaskQueue_SplitLargestIfNeeded(t *testing.T) {
	q := NewTaskQueue(types.MinChunk)

	// Add a task large enough to split (> 2*MinChunk)
	largeTask := types.Task{Offset: 0, Length: 10 * types.MB} // 10MB, should be splittable
	q.Push(largeTask)

	split := q.SplitLargestIfNeeded()

	if !split {
		t.Error("Should have split the large task")
	}
	if q.Len() != 2 {
		t.Errorf("Queue length after split = %d, want 2", q.Len())
	}
}

func TestTaskQueue_SplitLargestIfNeeded_TooSmall(t *testing.T) {
	q := NewTaskQueue(types.MinChunk)

	// Add a task too small to split (< 2*MinChunk)
	smallTask := types.Task{Offset: 0, Length: types.MinChunk} // Exactly MinChunk, shouldn't split
	q.Push(smallTask)

	split := q.SplitLargestIfNeeded()

	if split {
		t.Error("Should not split a task smaller than 2*MinChunk")
	}
	if q.Len() != 1 {
		t.Errorf("Queue length = %d, want 1", q.Len())
	}
}

func TestTask_Struct(t *testing.T) {
	task := types.Task{Offset: 100, Length: 500}

	if task.Offset != 100 {
		t.Errorf("Offset = %d, want 100", task.Offset)
	}
	if task.Length != 500 {
		t.Errorf("Length = %d, want 500", task.Length)
	}
}

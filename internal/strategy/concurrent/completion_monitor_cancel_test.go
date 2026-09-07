package concurrent

import (
	"context"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/SurgeDM/Surge/internal/progress"
	"github.com/SurgeDM/Surge/internal/testutil"
	"github.com/SurgeDM/Surge/internal/transport"
	"github.com/SurgeDM/Surge/internal/types"
)

func TestRunCompletionMonitor_DownloadedDoesNotCancelActives(t *testing.T) {
	const fileSize int64 = 1024
	queue := NewTaskQueue()
	state := progress.New("monitor-downloaded-not-key", fileSize)
	state.Bytes.Downloaded.Store(fileSize)

	taskCtx1, cancel1 := context.WithCancel(context.Background())
	taskCtx2, cancel2 := context.WithCancel(context.Background())
	defer cancel1()
	defer cancel2()

	d := &ConcurrentDownloader{
		State:       state,
		activeTasks: map[int]*ActiveTask{0: {Cancel: cancel1}, 1: {Cancel: cancel2}},
	}

	monCtx, monCancel := context.WithCancel(context.Background())
	defer monCancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.runCompletionMonitor(monCtx, queue, fileSize, 4)
	}()

	select {
	case <-done:
		t.Fatal("completion monitor returned on Downloaded without VerifiedProgress")
	case <-time.After(300 * time.Millisecond):
	}

	select {
	case <-taskCtx1.Done():
		t.Fatal("active task 0 was cancelled on Downloaded-full VP-short")
	default:
	}
	select {
	case <-taskCtx2.Done():
		t.Fatal("active task 1 was cancelled on Downloaded-full VP-short")
	default:
	}

	monCancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("completion monitor did not exit after parent cancel")
	}
}

func TestRunCompletionMonitor_IdleCompletionStillCloses(t *testing.T) {
	queue := NewTaskQueue()
	d := &ConcurrentDownloader{activeTasks: map[int]*ActiveTask{}}

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		_, _ = queue.Pop()
	}()
	deadline := time.Now().Add(time.Second)
	for queue.IdleWorkers() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if queue.IdleWorkers() != 1 {
		t.Fatal("queue worker did not become idle")
	}

	monCtx, monCancel := context.WithCancel(context.Background())
	defer monCancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.runCompletionMonitor(monCtx, queue, 1, 1)
	}()

	select {
	case <-workerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("idle completion did not close the queue")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("completion monitor did not exit on idle completion")
	}
}

func TestRunCompletionMonitor_VerifiedProgressCancelsActivesAndDrains(t *testing.T) {
	const fileSize int64 = 1024
	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 0, Length: 100})
	queue.Push(types.Task{Offset: 100, Length: 100})
	state := progress.New("monitor-vp-done", fileSize)
	state.Bytes.VerifiedProgress.Store(fileSize)
	state.Bytes.Downloaded.Store(0)

	taskCtx1, cancel1 := context.WithCancel(context.Background())
	taskCtx2, cancel2 := context.WithCancel(context.Background())
	defer cancel1()
	defer cancel2()

	d := &ConcurrentDownloader{
		State:       state,
		activeTasks: map[int]*ActiveTask{0: {Cancel: cancel1}, 1: {Cancel: cancel2}},
	}

	monCtx, monCancel := context.WithCancel(context.Background())
	defer monCancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.runCompletionMonitor(monCtx, queue, fileSize, 4)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("completion monitor did not return after VerifiedProgress reached file size")
	}

	select {
	case <-taskCtx1.Done():
	default:
		t.Fatal("active task 0 was not cancelled")
	}
	select {
	case <-taskCtx2.Done():
	default:
		t.Fatal("active task 1 was not cancelled")
	}
	if remaining := queue.DrainRemaining(); len(remaining) != 0 {
		t.Fatalf("VP path left %d queued tasks: %+v", len(remaining), remaining)
	}
	if !d.completing.Load() {
		t.Fatal("completing was not set on the VP path")
	}
}

func TestRunCompletionMonitor_IdleVPShortDoesNotComplete(t *testing.T) {
	const fileSize int64 = 1024
	queue := NewTaskQueue()
	state := progress.New("monitor-idle-vp-short", fileSize)

	d := &ConcurrentDownloader{
		State:       state,
		activeTasks: map[int]*ActiveTask{},
	}

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		_, _ = queue.Pop()
	}()
	deadline := time.Now().Add(time.Second)
	for queue.IdleWorkers() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if queue.IdleWorkers() != 1 {
		t.Fatal("queue worker did not become idle")
	}

	monCtx, monCancel := context.WithCancel(context.Background())
	defer monCancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.runCompletionMonitor(monCtx, queue, fileSize, 1)
	}()

	select {
	case <-done:
		t.Fatal("completion monitor returned while idle with VP below file size")
	case <-time.After(300 * time.Millisecond):
	}

	monCancel()
	select {
	case <-workerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("idle waiter did not unblock after parent cancel")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("completion monitor did not exit after parent cancel")
	}
	if d.completing.Load() {
		t.Fatal("parent cancel must not set completing")
	}
}

func TestRunCompletionMonitor_NilCancelDoesNotPanic(t *testing.T) {
	const fileSize int64 = 1024
	queue := NewTaskQueue()
	state := progress.New("monitor-nil-cancel", fileSize)
	state.Bytes.VerifiedProgress.Store(fileSize)

	taskCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := &ConcurrentDownloader{
		State: state,
		activeTasks: map[int]*ActiveTask{
			0: {},
			1: {Cancel: nil},
			2: {Cancel: cancel},
		},
	}

	monCtx, monCancel := context.WithCancel(context.Background())
	defer monCancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.runCompletionMonitor(monCtx, queue, fileSize, 4)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("completion monitor panicked or hung on nil Cancel")
	}
	select {
	case <-taskCtx.Done():
	default:
		t.Fatal("non-nil Cancel was not invoked")
	}
}

func TestRunCompletionMonitor_CompletionCancelDoesNotRequeue(t *testing.T) {
	const fileSize int64 = 1024
	var startedOnce sync.Once
	started := make(chan struct{})
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-1023/1024")
		w.WriteHeader(http.StatusPartialContent)
		w.(http.Flusher).Flush()
		startedOnce.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	defer server.Close()

	outFile, err := os.CreateTemp(t.TempDir(), "completion-no-requeue-*.surge")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = outFile.Close() }()

	state := progress.New("completion-no-requeue", fileSize)
	d := NewConcurrentDownloader("completion-no-requeue", nil, state, &types.RuntimeConfig{
		MaxTaskRetries: 3,
	})
	d.hostLimiter = transport.NewHostRateLimiter()

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 0, Length: fileSize})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	workerErr := make(chan error, 1)
	go func() {
		workerErr <- d.worker(ctx, 0, []string{server.URL}, outFile, queue, fileSize, server.Client())
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start the tarpit GET")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		d.activeMu.Lock()
		at := d.activeTasks[0]
		registered := at != nil && at.Cancel != nil
		d.activeMu.Unlock()
		if registered {
			break
		}
		time.Sleep(time.Millisecond)
	}

	state.Bytes.VerifiedProgress.Store(fileSize)
	monDone := make(chan struct{})
	go func() {
		defer close(monDone)
		d.runCompletionMonitor(ctx, queue, fileSize, 4)
	}()

	select {
	case err := <-workerErr:
		if err != nil {
			t.Fatalf("worker err=%v, want nil after completion cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker hung after completion cancel (likely requeued onto the tarpit)")
	}

	select {
	case <-monDone:
	case <-time.After(time.Second):
		t.Fatal("completion monitor did not exit")
	}

	if remaining := queue.DrainRemaining(); len(remaining) != 0 {
		t.Fatalf("completion cancel requeued %d tasks: %+v", len(remaining), remaining)
	}
}

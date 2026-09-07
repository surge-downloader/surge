package concurrent

import (
	"context"
	"testing"
	"time"

	"github.com/SurgeDM/Surge/internal/progress"
	"github.com/SurgeDM/Surge/internal/transport"
	"github.com/SurgeDM/Surge/internal/types"
)

func TestWorker_VPEarlyExitReleasesGateWithoutInsert(t *testing.T) {
	const totalSize int64 = 1024
	state := progress.New("vp-early-exit", totalSize)
	state.Bytes.VerifiedProgress.Store(totalSize)

	d := NewConcurrentDownloader("vp-early-exit", nil, state, &types.RuntimeConfig{})
	d.hostLimiter = transport.NewHostRateLimiter()
	d.concurrencyGate = newAdaptiveConcurrencyGate(1, 0)

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 0, Length: totalSize})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := d.worker(ctx, 0, []string{"http://127.0.0.1:1"}, nil, queue, totalSize, nil); err != nil {
		t.Fatalf("worker err=%v, want nil", err)
	}

	d.activeMu.Lock()
	n := len(d.activeTasks)
	d.activeMu.Unlock()
	if n != 0 {
		t.Fatalf("activeTasks len=%d, want 0 (failed path must not insert)", n)
	}
	if got := state.ActiveWorkers.Load(); got != 0 {
		t.Fatalf("ActiveWorkers=%d, want 0", got)
	}
	if remaining := queue.DrainRemaining(); len(remaining) != 0 {
		t.Fatalf("popped task was pushed back: %+v", remaining)
	}

	acqCtx, acqCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer acqCancel()
	if !d.concurrencyGate.acquire(acqCtx) {
		t.Fatal("gate permit leaked: subsequent acquire did not succeed")
	}
	d.concurrencyGate.release()
}

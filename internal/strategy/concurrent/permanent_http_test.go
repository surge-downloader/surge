package concurrent

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SurgeDM/Surge/internal/testutil"
	"github.com/SurgeDM/Surge/internal/transport"
	"github.com/SurgeDM/Surge/internal/types"
)

func TestDownloadTask_PermanentStatusMatrix(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		permanent   bool
		soft403     bool
		rateLimited bool
	}{
		{"401", http.StatusUnauthorized, true, false, false},
		{"403", http.StatusForbidden, false, true, false},
		{"404", http.StatusNotFound, true, false, false},
		{"410", http.StatusGone, true, false, false},
		{"416", http.StatusRequestedRangeNotSatisfiable, true, false, false},
		{"429", http.StatusTooManyRequests, false, false, true},
		{"500", http.StatusInternalServerError, false, false, false},
		{"503", http.StatusServiceUnavailable, false, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer server.Close()

			outFile, err := os.CreateTemp(t.TempDir(), "permanent-status-*.surge")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = outFile.Close() }()

			d := NewConcurrentDownloader("permanent-status-"+tc.name, nil, nil, &types.RuntimeConfig{
				MaxTaskRetries: 1,
			})
			task := types.Task{Offset: 0, Length: 1024}
			active := &ActiveTask{Task: task}
			active.CurrentOffset.Store(task.Offset)
			active.StopAt.Store(task.Offset + task.Length)

			err = d.downloadTask(
				context.Background(),
				server.URL,
				outFile,
				active,
				make([]byte, 32*1024),
				server.Client(),
				1024,
			)
			if err == nil {
				t.Fatal("expected error for non-success status")
			}

			if got := types.IsPermanentHTTPError(err); got != tc.permanent {
				t.Fatalf("IsPermanentHTTPError=%v, want %v (err=%v)", got, tc.permanent, err)
			}
			if got := errors.Is(err, errSoftForbidden); got != tc.soft403 {
				t.Fatalf("errSoftForbidden=%v, want %v (err=%v)", got, tc.soft403, err)
			}
			var rlErr *rateLimitError
			if got := errors.As(err, &rlErr); got != tc.rateLimited {
				t.Fatalf("rateLimitError=%v, want %v (err=%v)", got, tc.rateLimited, err)
			}
		})
	}
}

func TestWorker_PermanentHTTPOneShot(t *testing.T) {
	statuses := []int{
		http.StatusNotFound,
		http.StatusUnauthorized,
		http.StatusRequestedRangeNotSatisfiable,
	}
	for _, status := range statuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			runPermanentHTTPWorker(t, status, false)
		})
	}
}

func TestWorker_PermanentHTTPReleasesConcurrencyGate(t *testing.T) {
	runPermanentHTTPWorker(t, http.StatusNotFound, true)
}

func runPermanentHTTPWorker(t *testing.T, status int, checkGate bool) {
	t.Helper()

	const rangeLen int64 = 1024
	var requests atomic.Int64
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(status)
	}))
	defer server.Close()

	outFile, err := os.CreateTemp(t.TempDir(), "permanent-http-worker-*.surge")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = outFile.Close() }()

	d := NewConcurrentDownloader("permanent-http-worker", nil, nil, &types.RuntimeConfig{
		MaxTaskRetries: 3,
	})
	d.hostLimiter = transport.NewHostRateLimiter()
	if checkGate {
		d.concurrencyGate = newAdaptiveConcurrencyGate(1, 0)
	}

	queue := NewTaskQueue()
	original := types.Task{Offset: 0, Length: rangeLen}
	queue.Push(original)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	err = d.worker(ctx, 0, []string{server.URL}, outFile, queue, rangeLen, server.Client())
	elapsed := time.Since(start)

	if !types.IsPermanentHTTPError(err) {
		t.Fatalf("worker err=%v, want permanent HTTP", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests=%d, want 1 (retries must not burn)", got)
	}
	if elapsed >= 2*types.RetryBaseDelay {
		t.Fatalf("elapsed %v, want < %v (no single-mirror backoff)", elapsed, types.RetryBaseDelay)
	}

	remaining := queue.DrainRemaining()
	if len(remaining) != 1 {
		t.Fatalf("residual tasks=%d, want 1", len(remaining))
	}
	if remaining[0].Offset != original.Offset || remaining[0].Length != original.Length {
		t.Fatalf("residual %+v, want untouched range %+v", remaining[0], original)
	}

	if checkGate {
		acquireCtx, acquireCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer acquireCancel()
		if !d.concurrencyGate.acquire(acquireCtx) {
			t.Fatal("concurrency gate permit leaked after permanent HTTP")
		}
		d.concurrencyGate.release()
	}
}

package concurrent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SurgeDM/Surge/internal/progress"
	"github.com/SurgeDM/Surge/internal/testutil"
	"github.com/SurgeDM/Surge/internal/transport"
	"github.com/SurgeDM/Surge/internal/types"
	"github.com/SurgeDM/Surge/internal/utils"
)

func TestDownloadTask_EarlyEOF_ChunkedPartial(t *testing.T) {
	const rangeLen int64 = 32 * 1024
	const partial int64 = 8 * 1024

	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end, err := parseTestByteRange(r.Header.Get("Range"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, rangeLen))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(make([]byte, partial))
		w.(http.Flusher).Flush()
	}))
	defer server.Close()

	outFile, err := os.CreateTemp(t.TempDir(), "early-eof-chunked-*.surge")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = outFile.Close() }()

	task := types.Task{Offset: 0, Length: rangeLen}
	active := &ActiveTask{Task: task}
	active.CurrentOffset.Store(task.Offset)
	active.StopAt.Store(task.Offset + task.Length)
	d := NewConcurrentDownloader("early-eof-chunked", nil, nil, &types.RuntimeConfig{})

	err = d.downloadTask(
		context.Background(),
		server.URL,
		outFile,
		active,
		make([]byte, types.WorkerBuffer),
		server.Client(),
		rangeLen,
	)
	if err == nil {
		t.Fatal("expected early EOF error")
	}
	if types.IsPermanentHTTPError(err) {
		t.Fatalf("early EOF must be retryable, got permanent: %v", err)
	}
	if !strings.Contains(err.Error(), "early EOF") {
		t.Fatalf("err=%v, want early EOF", err)
	}
	if got, want := active.CurrentOffset.Load(), active.StopAt.Load(); got >= want {
		t.Fatalf("offset %d >= StopAt %d, want a short read", got, want)
	}
}

func TestDownloadTask_EarlyEOF_UnexpectedEOFNotMasked(t *testing.T) {
	const rangeLen int64 = 32 * 1024
	const partial int64 = 8 * 1024

	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(rangeLen, 10))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", rangeLen-1, rangeLen))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(make([]byte, partial))
	}))
	defer server.Close()

	outFile, err := os.CreateTemp(t.TempDir(), "early-eof-unexpected-*.surge")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = outFile.Close() }()

	task := types.Task{Offset: 0, Length: rangeLen}
	active := &ActiveTask{Task: task}
	active.CurrentOffset.Store(task.Offset)
	active.StopAt.Store(task.Offset + task.Length)
	d := NewConcurrentDownloader("early-eof-unexpected", nil, nil, &types.RuntimeConfig{})

	err = d.downloadTask(
		context.Background(),
		server.URL,
		outFile,
		active,
		make([]byte, types.WorkerBuffer),
		server.Client(),
		rangeLen,
	)
	if err == nil {
		t.Fatal("expected read error")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err=%v, want io.ErrUnexpectedEOF", err)
	}
	if !strings.Contains(err.Error(), "read error:") {
		t.Fatalf("err=%v, want read error: prefix", err)
	}
	if strings.Contains(err.Error(), "early EOF") {
		t.Fatalf("unexpected EOF must not be masked as early EOF: %v", err)
	}
}

func TestEarlyEOF_DownloadFailsOverToHealthyMirror(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(256 * utils.KiB)
	partial := int64(64 * utils.KiB)
	var eofHits atomic.Int64

	eofServer := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		eofHits.Add(1)
		start, end, err := parseTestByteRange(r.Header.Get("Range"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
		w.WriteHeader(http.StatusPartialContent)
		toSend := partial
		if remaining := end - start + 1; toSend > remaining {
			toSend = remaining
		}
		_, _ = w.Write(make([]byte, toSend))
		w.(http.Flusher).Flush()
	}))
	defer eofServer.Close()

	healthy := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer healthy.Close()

	destPath := filepath.Join(tmpDir, "early_eof_failover.bin")
	workingPath := destPath + types.IncompleteSuffix
	if f, err := os.Create(workingPath); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}

	state := progress.New("early-eof-failover", fileSize)
	d := NewConcurrentDownloader("early-eof-failover", nil, state, &types.RuntimeConfig{
		MaxConnectionsPerDownload: 1,
		Workers:                   1,
		MinChunkSize:              fileSize,
		MaxTaskRetries:            2,
		DialHedgeCount:            0,
	})
	d.hostLimiter = transport.NewHostRateLimiter()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := d.Download(ctx, eofServer.URL, []string{healthy.URL()}, []string{healthy.URL()}, destPath, fileSize)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}
	if eofHits.Load() < 1 {
		t.Fatal("expected the early-EOF primary to be contacted")
	}
	if got := state.Bytes.Downloaded.Load(); got != fileSize {
		t.Errorf("Downloaded = %d, want %d", got, fileSize)
	}
	if err := testutil.VerifyFileSize(workingPath, fileSize); err != nil {
		t.Error(err)
	}
}

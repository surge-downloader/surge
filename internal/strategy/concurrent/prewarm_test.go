package concurrent

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SurgeDM/Surge/internal/progress"
	"github.com/SurgeDM/Surge/internal/testutil"
	"github.com/SurgeDM/Surge/internal/types"
	"github.com/SurgeDM/Surge/internal/utils"
)

func TestConcurrentDownloader_PrewarmConnections(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1 * utils.MiB)
	destPath := filepath.Join(tmpDir, "prewarm_test.bin")

	var mu sync.Mutex
	prewarmSeen := false
	downloadSeen := false

	// Create mock server to track request order
	server := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
		testutil.WithHandler(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			rng := r.Header.Get("Range")
			if rng == "bytes=0-0" {
				prewarmSeen = true
			} else if rng != "" {
				downloadSeen = true
			}
			mu.Unlock()

			serveRequestedRange(w, r, fileSize)
		}),
	)
	defer server.Close()

	// Ensure incomplete file exists
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	}

	state := progress.New("prewarm-test", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		DialHedgeCount:            2, // Enable hedging
		MinChunkSize:              256 * utils.KiB,
	}

	downloader := NewConcurrentDownloader("prewarm-id", nil, state, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := downloader.Download(ctx, server.URL(), []string{server.URL()}, []string{server.URL()}, destPath, fileSize)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if !prewarmSeen {
		t.Error("Expected to see pre-warm request (bytes=0-0), but none were recorded")
	}
	if !downloadSeen {
		t.Error("Expected to see download requests, but none were recorded")
	}
}

func serveRequestedRange(w http.ResponseWriter, r *http.Request, fileSize int64) {
	rng := r.Header.Get("Range")
	if rng == "" {
		w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, fileSize))
		return
	}

	start, end, err := parseInclusiveByteRange(rng, fileSize)
	if err != nil {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}

	length := end - start + 1
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.WriteHeader(http.StatusPartialContent)
	if _, err := w.Write(make([]byte, length)); err != nil {
		return
	}
}

func parseInclusiveByteRange(rangeHeader string, fileSize int64) (int64, int64, error) {
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return 0, 0, fmt.Errorf("invalid range prefix")
	}

	parts := strings.Split(strings.TrimPrefix(rangeHeader, "bytes="), "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid range format")
	}

	var start, end int64
	var err error
	if parts[0] == "" {
		end = fileSize - 1
		start, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return 0, 0, err
		}
		start = fileSize - start
	} else {
		start, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return 0, 0, err
		}
		if parts[1] == "" {
			end = fileSize - 1
		} else {
			end, err = strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				return 0, 0, err
			}
		}
	}

	if start < 0 || end >= fileSize || start > end {
		return 0, 0, fmt.Errorf("range out of bounds")
	}
	return start, end, nil
}

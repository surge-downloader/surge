package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/surge-downloader/surge/internal/engine/concurrent"
	"github.com/surge-downloader/surge/internal/engine/state"
	"github.com/surge-downloader/surge/internal/engine/types"
)

const (
	FileSize = 2 * 1024 * 1024 * 1024 // 2 GB
)

func main() {
	// 1. Start High-Speed Local Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", FileSize))

		http.ServeContent(w, r, "bench.bin", time.Now(), &ZeroReader{Size: FileSize})
	}))
	defer ts.Close()

	fmt.Printf("Benchmark Server running at %s\n", ts.URL)

	// 2. Configure Downloader
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerHost: 32,
		WorkerBufferSize:      4 * 1024 * 1024, // 4MB buffer
	}

	progressCh := make(chan any, 100)
	stateDesc := types.NewProgressState("bench-1", FileSize)

	// 3. Output to /dev/shm to avoid disk IO bottleneck
	// If /dev/shm not available, use temp dir
	destDir := "/dev/shm"
	if _, err := os.Stat(destDir); err != nil {
		destDir = os.TempDir()
	}

	// Configure State DB
	dbPath := filepath.Join(destDir, "surge-bench.db")
	state.Configure(dbPath)

	destPath := filepath.Join(destDir, "surge-bench.bin")

	// Cleanup previous
	os.Remove(destPath)
	os.Remove(destPath + ".surge") // Incomplete suffix
	os.Remove(dbPath)              // Cleanup DB

	downloader := concurrent.NewConcurrentDownloader("bench-1", progressCh, stateDesc, runtime)

	fmt.Printf("Downloading %d MB to %s...\n", FileSize/1024/1024, destPath)

	start := time.Now()
	err := downloader.Download(context.Background(), ts.URL, destPath, FileSize, false)
	duration := time.Since(start)

	if err != nil {
		fmt.Fprintf(os.Stderr, "Download failed: %v\n", err)
		os.Exit(1)
	}

	// 4. Report
	mbps := float64(FileSize) / 1024 / 1024 / duration.Seconds()
	fmt.Printf("Download completed in %v\n", duration)
	fmt.Printf("Average Speed: %.2f MB/s\n", mbps)

	// Cleanup
	os.Remove(destPath)
	os.Remove(dbPath)
}

// ZeroReader implements io.ReadSeeker for zeros
type ZeroReader struct {
	Size int64
	pos  int64
}

func (z *ZeroReader) Read(p []byte) (n int, err error) {
	if z.pos >= z.Size {
		return 0, io.EOF
	}
	remaining := z.Size - z.pos
	if int64(len(p)) > remaining {
		n = int(remaining)
	} else {
		n = len(p)
	}
	z.pos += int64(n)
	return n, nil
}

func (z *ZeroReader) Seek(offset int64, whence int) (int64, error) {
	var newPos int64
	switch whence {
	case io.SeekStart:
		newPos = offset
	case io.SeekCurrent:
		newPos = z.pos + offset
	case io.SeekEnd:
		newPos = z.Size + offset
	}
	if newPos < 0 {
		return 0, fmt.Errorf("invalid seek")
	}
	z.pos = newPos
	return newPos, nil
}

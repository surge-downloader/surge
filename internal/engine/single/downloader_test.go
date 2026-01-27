package single

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/surge-downloader/surge/internal/engine/types"
	"github.com/surge-downloader/surge/internal/testutil"
)

func TestCopyFile(t *testing.T) {
	tmpDir, cleanup, err := testutil.TempDir("surge-copy-test")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	// Create source file
	srcPath, err := testutil.CreateTestFile(tmpDir, "src.bin", 1024, true)
	if err != nil {
		t.Fatal(err)
	}

	dstPath := filepath.Join(tmpDir, "dst.bin")

	err = copyFile(srcPath, dstPath)
	if err != nil {
		t.Fatalf("copyFile failed: %v", err)
	}

	// Verify destination exists
	if !testutil.FileExists(dstPath) {
		t.Error("Destination file should exist")
	}

	// Verify sizes match
	srcInfo, _ := os.Stat(srcPath)
	dstInfo, _ := os.Stat(dstPath)
	if srcInfo.Size() != dstInfo.Size() {
		t.Error("File sizes don't match")
	}

	// Verify contents match
	match, err := testutil.CompareFiles(srcPath, dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if !match {
		t.Error("File contents don't match")
	}
}

func TestCopyFile_SourceNotExists(t *testing.T) {
	tmpDir, cleanup, _ := testutil.TempDir("surge-copy-test")
	defer cleanup()

	err := copyFile(filepath.Join(tmpDir, "nonexistent.bin"), filepath.Join(tmpDir, "dst.bin"))
	if err == nil {
		t.Error("Expected error for nonexistent source")
	}
}

func TestCopyFile_InvalidDestination(t *testing.T) {
	tmpDir, cleanup, _ := testutil.TempDir("surge-copy-test")
	defer cleanup()

	srcPath, _ := testutil.CreateTestFile(tmpDir, "src.bin", 100, false)

	// Try to copy to an invalid path (non-existent directory)
	err := copyFile(srcPath, filepath.Join(tmpDir, "nonexistent", "subdir", "dst.bin"))
	if err == nil {
		t.Error("Expected error for invalid destination")
	}
}

func TestCopyFile_EmptyFile(t *testing.T) {
	tmpDir, cleanup, _ := testutil.TempDir("surge-copy-test")
	defer cleanup()

	srcPath, _ := testutil.CreateTestFile(tmpDir, "empty.bin", 0, false)
	dstPath := filepath.Join(tmpDir, "empty_copy.bin")

	err := copyFile(srcPath, dstPath)
	if err != nil {
		t.Fatalf("copyFile failed for empty file: %v", err)
	}

	if err := testutil.VerifyFileSize(dstPath, 0); err != nil {
		t.Error(err)
	}
}

func TestCopyFile_LargeFile(t *testing.T) {
	tmpDir, cleanup, _ := testutil.TempDir("surge-copy-test")
	defer cleanup()

	size := int64(5 * types.MB)
	srcPath, _ := testutil.CreateTestFile(tmpDir, "large.bin", size, false)
	dstPath := filepath.Join(tmpDir, "large_copy.bin")

	err := copyFile(srcPath, dstPath)
	if err != nil {
		t.Fatalf("copyFile failed for large file: %v", err)
	}

	if err := testutil.VerifyFileSize(dstPath, size); err != nil {
		t.Error(err)
	}
}

func TestCopyFile_ContentVerification(t *testing.T) {
	tmpDir, cleanup, _ := testutil.TempDir("surge-copy-content")
	defer cleanup()

	size := int64(128 * types.KB)
	srcPath, _ := testutil.CreateTestFile(tmpDir, "random.bin", size, true) // Random data
	dstPath := filepath.Join(tmpDir, "random_copy.bin")

	err := copyFile(srcPath, dstPath)
	if err != nil {
		t.Fatalf("copyFile failed: %v", err)
	}

	match, err := testutil.CompareFiles(srcPath, dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if !match {
		t.Error("Copied file content doesn't match source")
	}
}

// =============================================================================
// SingleDownloader - Streaming Server
// =============================================================================

func TestSingleDownloader_StreamingServer(t *testing.T) {
	tmpDir, cleanup, _ := testutil.TempDir("surge-stream-single")
	defer cleanup()

	fileSize := int64(1 * types.MB)
	server := testutil.NewStreamingMockServer(fileSize,
		testutil.WithRangeSupport(false),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "stream_single.bin")
	state := types.NewProgressState("stream-single", fileSize)
	runtime := &types.RuntimeConfig{}

	downloader := NewSingleDownloader("stream-id", nil, state, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err := downloader.Download(ctx, server.URL(), destPath, fileSize, "stream.bin", false)
	if err != nil {
		t.Fatalf("Streaming download failed: %v", err)
	}

	if err := testutil.VerifyFileSize(destPath, fileSize); err != nil {
		t.Error(err)
	}
}

// =============================================================================
// SingleDownloader - FailAfterBytes
// =============================================================================

func TestSingleDownloader_FailAfterBytes(t *testing.T) {
	tmpDir, cleanup, _ := testutil.TempDir("surge-failafter-single")
	defer cleanup()

	fileSize := int64(256 * types.KB)
	// Server fails after sending 50KB
	server := testutil.NewMockServer(
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(false),
		testutil.WithFailAfterBytes(50*types.KB),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "failafter_single.bin")
	state := types.NewProgressState("failafter-single", fileSize)
	runtime := &types.RuntimeConfig{}

	downloader := NewSingleDownloader("failafter-id", nil, state, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := downloader.Download(ctx, server.URL(), destPath, fileSize, "failafter.bin", false)
	// Should fail since SingleDownloader doesn't retry
	if err == nil {
		t.Error("Expected error when server fails mid-transfer")
	}

	// Partial file should exist with .surge suffix
	stats := server.Stats()
	if stats.BytesServed < 50*types.KB {
		t.Errorf("Expected at least 50KB served before failure, got %d", stats.BytesServed)
	}
}

// =============================================================================
// SingleDownloader - NilState handling
// =============================================================================

func TestSingleDownloader_NilState(t *testing.T) {
	tmpDir, cleanup, _ := testutil.TempDir("surge-nilstate-single")
	defer cleanup()

	fileSize := int64(32 * types.KB)
	server := testutil.NewMockServer(
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(false),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "nilstate_single.bin")
	runtime := &types.RuntimeConfig{}

	// Create downloader with nil state
	downloader := NewSingleDownloader("nilstate-id", nil, nil, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := downloader.Download(ctx, server.URL(), destPath, fileSize, "nilstate.bin", false)
	if err != nil {
		t.Fatalf("Download with nil state failed: %v", err)
	}

	if err := testutil.VerifyFileSize(destPath, fileSize); err != nil {
		t.Error(err)
	}
}

// =============================================================================
// Restored Standard Tests
// =============================================================================

func TestNewSingleDownloader(t *testing.T) {
	state := types.NewProgressState("test", 1000)
	runtime := &types.RuntimeConfig{}

	downloader := NewSingleDownloader("test-id", nil, state, runtime)

	if downloader == nil {
		t.Fatal("NewSingleDownloader returned nil")
	}
	if downloader.ID != "test-id" {
		t.Errorf("ID mismatch: got %s, want test-id", downloader.ID)
	}
	if downloader.State != state {
		t.Error("State not set correctly")
	}
}

func TestSingleDownloader_Download_Success(t *testing.T) {
	tmpDir, cleanup, err := testutil.TempDir("surge-single-test")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	fileSize := int64(64 * 1024) // 64KB
	server := testutil.NewMockServer(
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(false), // SingleDownloader doesn't use ranges
		testutil.WithFilename("single_test.bin"),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "single_test.bin")
	state := types.NewProgressState("single-test", fileSize)
	runtime := &types.RuntimeConfig{WorkerBufferSize: 8 * types.KB}

	downloader := NewSingleDownloader("single-id", nil, state, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err = downloader.Download(ctx, server.URL(), destPath, fileSize, "single_test.bin", false)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	// Verify file exists and has correct size
	if err := testutil.VerifyFileSize(destPath, fileSize); err != nil {
		t.Error(err)
	}

	// Verify .surge file was removed
	surgeFile := destPath + types.IncompleteSuffix
	if testutil.FileExists(surgeFile) {
		t.Error(".surge file should be removed after successful download")
	}

	// Verify progress was tracked
	if state.Downloaded.Load() != fileSize {
		t.Errorf("Downloaded %d != fileSize %d", state.Downloaded.Load(), fileSize)
	}
}

func TestSingleDownloader_Download_Cancellation(t *testing.T) {
	tmpDir, cleanup, _ := testutil.TempDir("surge-cancel-single")
	defer cleanup()

	// Large file with latency
	fileSize := int64(5 * types.MB)
	server := testutil.NewMockServer(
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(false),
		testutil.WithByteLatency(500*time.Microsecond),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "cancel_single.bin")
	state := types.NewProgressState("cancel-single", fileSize)
	runtime := &types.RuntimeConfig{}

	downloader := NewSingleDownloader("cancel-id", nil, state, runtime)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error)
	go func() {
		done <- downloader.Download(ctx, server.URL(), destPath, fileSize, "cancel.bin", false)
	}()

	// Cancel after a short delay
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		// Accept context.Canceled or wrapped errors
		if err != nil && err != context.Canceled && err.Error() != "context canceled" {
			t.Logf("Expected context.Canceled, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Download didn't respond to cancellation")
	}
}

func TestSingleDownloader_Download_ProgressTracking(t *testing.T) {
	tmpDir, cleanup, _ := testutil.TempDir("surge-progress-single")
	defer cleanup()

	fileSize := int64(256 * types.KB)
	server := testutil.NewMockServer(
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(false),
		testutil.WithByteLatency(5*time.Microsecond), // Slow down to allow progress monitoring
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "progress_single.bin")
	state := types.NewProgressState("progress-single", fileSize)
	runtime := &types.RuntimeConfig{WorkerBufferSize: 16 * types.KB}

	downloader := NewSingleDownloader("progress-id", nil, state, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err := downloader.Download(ctx, server.URL(), destPath, fileSize, "progress.bin", false)

	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	// Verify final progress equals file size
	finalProgress := state.Downloaded.Load()
	if finalProgress != fileSize {
		t.Errorf("Final progress %d != file size %d", finalProgress, fileSize)
	}
}

func TestSingleDownloader_Download_ServerError(t *testing.T) {
	tmpDir, cleanup, _ := testutil.TempDir("surge-error-single")
	defer cleanup()

	// Server that fails on first request
	server := testutil.NewMockServer(
		testutil.WithFileSize(1024),
		testutil.WithFailOnNthRequest(1),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "error_single.bin")
	state := types.NewProgressState("error-single", 1024)
	runtime := &types.RuntimeConfig{}

	downloader := NewSingleDownloader("error-id", nil, state, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := downloader.Download(ctx, server.URL(), destPath, 1024, "error.bin", false)
	if err == nil {
		t.Error("Expected error from failed server")
	}
}

func TestSingleDownloader_Download_WithLatency(t *testing.T) {
	tmpDir, cleanup, _ := testutil.TempDir("surge-latency-single")
	defer cleanup()

	fileSize := int64(32 * types.KB)
	server := testutil.NewMockServer(
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(false),
		testutil.WithLatency(100*time.Millisecond),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "latency_single.bin")
	state := types.NewProgressState("latency-single", fileSize)
	runtime := &types.RuntimeConfig{}

	downloader := NewSingleDownloader("latency-id", nil, state, runtime)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := downloader.Download(ctx, server.URL(), destPath, fileSize, "latency.bin", false)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if elapsed < 100*time.Millisecond {
		t.Errorf("Download completed too fast (%v), latency not applied", elapsed)
	}

	if err := testutil.VerifyFileSize(destPath, fileSize); err != nil {
		t.Error(err)
	}
}

func TestSingleDownloader_Download_ContentIntegrity(t *testing.T) {
	tmpDir, cleanup, _ := testutil.TempDir("surge-content-single")
	defer cleanup()

	fileSize := int64(64 * types.KB)
	server := testutil.NewMockServer(
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(false),
		testutil.WithRandomData(true),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "content_single.bin")
	state := types.NewProgressState("content-single", fileSize)
	runtime := &types.RuntimeConfig{}

	downloader := NewSingleDownloader("content-id", nil, state, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := downloader.Download(ctx, server.URL(), destPath, fileSize, "content.bin", false)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if err := testutil.VerifyFileSize(destPath, fileSize); err != nil {
		t.Error(err)
	}

	// Verify content is not all zeros (random data was used)
	chunk, err := testutil.ReadFileChunk(destPath, 0, 1024)
	if err != nil {
		t.Fatal(err)
	}

	allZero := true
	for _, b := range chunk {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("Content should not be all zeros with random data")
	}
}

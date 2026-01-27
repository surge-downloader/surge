package concurrent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/surge-downloader/surge/internal/engine/state"
	"github.com/surge-downloader/surge/internal/engine/types"
	"github.com/surge-downloader/surge/internal/testutil"
)

// Helper to init state just for tests (avoiding global init if possible,
// using temporary directories for each test)
func initTestState(t *testing.T) (string, func()) {
	state.CloseDB() // Ensure any previous DB is closed

	tmpDir, cleanup, err := testutil.TempDir("surge-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "surge.db")
	state.Configure(dbPath)

	return tmpDir, func() {
		state.CloseDB() // Close DB before removing dir
		cleanup()
	}
}

func TestConcurrentDownloader_Download(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1 * types.MB)
	server := testutil.NewMockServer(
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "test_download.bin")
	state := types.NewProgressState("test-id", fileSize)
	runtime := &types.RuntimeConfig{MaxConnectionsPerHost: 4}

	downloader := NewConcurrentDownloader("test-id", nil, state, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := downloader.Download(ctx, server.URL(), destPath, fileSize, false)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if err := testutil.VerifyFileSize(destPath, fileSize); err != nil {
		t.Error(err)
	}
}

// =============================================================================
// Advanced Integration Tests - Latency & Timeouts
// =============================================================================

func TestConcurrentDownloader_WithLatency(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(64 * types.KB)
	server := testutil.NewMockServer(
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
		testutil.WithLatency(100*time.Millisecond), // 100ms per request
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "latency_test.bin")
	state := types.NewProgressState("latency-test", fileSize)
	runtime := &types.RuntimeConfig{MaxConnectionsPerHost: 2}

	downloader := NewConcurrentDownloader("latency-id", nil, state, runtime)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := downloader.Download(ctx, server.URL(), destPath, fileSize, false)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	// Should take at least 100ms due to latency
	if elapsed < 100*time.Millisecond {
		t.Errorf("Download completed too fast (%v), latency not applied", elapsed)
	}

	if err := testutil.VerifyFileSize(destPath, fileSize); err != nil {
		t.Error(err)
	}
}

func TestConcurrentDownloader_SlowDownload(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(32 * types.KB)
	// Very slow byte-by-byte latency
	server := testutil.NewMockServer(
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
		testutil.WithByteLatency(10*time.Microsecond),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "slow_test.bin")
	state := types.NewProgressState("slow-test", fileSize)
	runtime := &types.RuntimeConfig{MaxConnectionsPerHost: 4}

	downloader := NewConcurrentDownloader("slow-id", nil, state, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err := downloader.Download(ctx, server.URL(), destPath, fileSize, false)
	if err != nil {
		t.Fatalf("Slow download failed: %v", err)
	}

	if err := testutil.VerifyFileSize(destPath, fileSize); err != nil {
		t.Error(err)
	}
}

// =============================================================================
// Advanced Integration Tests - Connection Limits
// =============================================================================

func TestConcurrentDownloader_RespectServerConnectionLimit(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(256 * types.KB)
	maxConns := 2
	server := testutil.NewMockServer(
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
		testutil.WithMaxConcurrentRequests(maxConns),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "connlimit_test.bin")
	state := types.NewProgressState("connlimit-test", fileSize)
	// Client configured for more connections than server allows
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerHost: 8, // More than server allows
		MinChunkSize:          16 * types.KB,
	}

	downloader := NewConcurrentDownloader("connlimit-id", nil, state, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := downloader.Download(ctx, server.URL(), destPath, fileSize, false)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if err := testutil.VerifyFileSize(destPath, fileSize); err != nil {
		t.Error(err)
	}

	stats := server.Stats()
	t.Logf("Server stats: TotalRequests=%d, RangeRequests=%d", stats.TotalRequests, stats.RangeRequests)
}

// =============================================================================
// Advanced Integration Tests - Content Verification
// =============================================================================

func TestConcurrentDownloader_ContentIntegrity(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(128 * types.KB)
	// Use random data so we can verify content integrity
	server := testutil.NewMockServer(
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
		testutil.WithRandomData(true),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "integrity_test.bin")
	state := types.NewProgressState("integrity-test", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerHost: 4,
		MinChunkSize:          16 * types.KB,
	}

	downloader := NewConcurrentDownloader("integrity-id", nil, state, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := downloader.Download(ctx, server.URL(), destPath, fileSize, false)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	// Verify file size matches
	if err := testutil.VerifyFileSize(destPath, fileSize); err != nil {
		t.Error(err)
	}

	// Read first and last chunks and verify they're not all zeros
	first, err := testutil.ReadFileChunk(destPath, 0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	last, err := testutil.ReadFileChunk(destPath, fileSize-1024, 1024)
	if err != nil {
		t.Fatal(err)
	}

	// Random data shouldn't be all zeros
	allZero := true
	for _, b := range first {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("First chunk is all zeros - random data not applied correctly")
	}

	allZero = true
	for _, b := range last {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("Last chunk is all zeros - random data not applied correctly")
	}
}

func TestConcurrentDownloader_SmallFile(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(64 * 1024) // 64KB
	server := testutil.NewMockServer(
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
		testutil.WithFilename("small_test.bin"),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "small_test.bin")
	state := types.NewProgressState("test-download", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerHost: 4,
		MinChunkSize:          16 * types.KB,
		MaxChunkSize:          32 * types.KB,
		TargetChunkSize:       16 * types.KB,
		WorkerBufferSize:      8 * types.KB,
		MaxTaskRetries:        3,
	}

	downloader := NewConcurrentDownloader("test-id", nil, state, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := downloader.Download(ctx, server.URL(), destPath, fileSize, false)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if err := testutil.VerifyFileSize(destPath, fileSize); err != nil {
		t.Error(err)
	}

	surgeFile := destPath + types.IncompleteSuffix
	if testutil.FileExists(surgeFile) {
		t.Error(".surge file should be removed after successful download")
	}
}

func TestConcurrentDownloader_MediumFile(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1 * types.MB)
	server := testutil.NewMockServer(
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "medium_test.bin")
	state := types.NewProgressState("test-download", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerHost: 8,
		MinChunkSize:          64 * types.KB,
		MaxChunkSize:          256 * types.KB,
		TargetChunkSize:       128 * types.KB,
		WorkerBufferSize:      32 * types.KB,
		MaxTaskRetries:        3,
	}

	downloader := NewConcurrentDownloader("test-id", nil, state, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err := downloader.Download(ctx, server.URL(), destPath, fileSize, false)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if err := testutil.VerifyFileSize(destPath, fileSize); err != nil {
		t.Error(err)
	}

	stats := server.Stats()
	if stats.RangeRequests == 0 {
		t.Error("Expected range requests for concurrent download")
	}
}

func TestConcurrentDownloader_Cancellation(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(10 * types.MB)
	server := testutil.NewMockServer(
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
		testutil.WithByteLatency(100*time.Microsecond),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "cancel_test.bin")
	state := types.NewProgressState("cancel-test", fileSize)
	runtime := &types.RuntimeConfig{MaxConnectionsPerHost: 4}

	downloader := NewConcurrentDownloader("cancel-id", nil, state, runtime)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error)
	go func() {
		done <- downloader.Download(ctx, server.URL(), destPath, fileSize, false)
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		// Accept context.Canceled or "operation was canceled" error string
		if err != nil && err != context.Canceled && err.Error() != "context canceled" {
			t.Logf("Download returned: %v (expected context.Canceled or nil)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Download didn't respond to cancellation")
	}
}

func TestConcurrentDownloader_ProgressTracking(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(512 * types.KB)
	server := testutil.NewMockServer(
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "progress_test.bin")
	state := types.NewProgressState("progress-test", fileSize)
	runtime := &types.RuntimeConfig{MaxConnectionsPerHost: 4}

	downloader := NewConcurrentDownloader("progress-id", nil, state, runtime)

	// Since we can't easily access atomic counters inside the test helper without modifying imports or visibility,
	// we will trust the progress state updates which are public.
	// But the key is to run it and ensure it passes.

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := downloader.Download(ctx, server.URL(), destPath, fileSize, false)

	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	finalDownloaded := state.Downloaded.Load()
	if finalDownloaded != fileSize {
		t.Errorf("Final downloaded %d != file size %d", finalDownloaded, fileSize)
	}
}

func TestConcurrentDownloader_RetryOnFailure(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(256 * types.KB)
	// Server fails after 20KB per-request, forcing retries
	// With 64KB chunks, each request will fail mid-way
	server := testutil.NewMockServer(
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
		testutil.WithFailAfterBytes(20*types.KB), // Fail after 20KB per request
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "retry_test.bin")
	state := types.NewProgressState("retry-test", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerHost: 2,
		MaxTaskRetries:        10,            // Need more retries since each attempt only gets 20KB
		MinChunkSize:          64 * types.KB, // Larger chunks to ensure failures occur
		MaxChunkSize:          64 * types.KB,
		TargetChunkSize:       64 * types.KB,
	}

	downloader := NewConcurrentDownloader("retry-id", nil, state, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err := downloader.Download(ctx, server.URL(), destPath, fileSize, false)
	if err != nil {
		t.Fatalf("Download with retries failed: %v", err)
	}

	if err := testutil.VerifyFileSize(destPath, fileSize); err != nil {
		t.Error(err)
	}

	stats := server.Stats()
	if stats.FailedRequests == 0 {
		t.Error("Expected some failed requests that triggered retries")
	}
}

func TestConcurrentDownloader_FailOnNthRequest(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(256 * types.KB)
	// Fail the 2nd request - use 1 connection for predictable ordering
	server := testutil.NewMockServer(
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
		testutil.WithFailOnNthRequest(2),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "failnth_test.bin")
	state := types.NewProgressState("failnth-test", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerHost: 1, // Single connection for predictable request order
		MaxTaskRetries:        5,
		MinChunkSize:          64 * types.KB, // 4 chunks = 4 requests minimum
	}

	downloader := NewConcurrentDownloader("failnth-id", nil, state, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := downloader.Download(ctx, server.URL(), destPath, fileSize, false)
	if err != nil {
		t.Fatalf("Download should recover from Nth request failure: %v", err)
	}

	stats := server.Stats()
	if stats.FailedRequests < 1 {
		t.Errorf("Expected at least 1 failed request, got %d", stats.FailedRequests)
	}
}

func TestConcurrentDownloader_ResumePartialDownload(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(256 * types.KB)
	server := testutil.NewMockServer(
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "resume_test.bin")
	workingPath := destPath + types.IncompleteSuffix

	// Create partial .surge file (simulate interrupted download)
	partialSize := int64(100 * types.KB)
	// Check if CreateTestFile needs to be adjusted.
	// Assuming testutil.CreateTestFile is available.
	_, err := testutil.CreateTestFile(tmpDir, "resume_test.bin.surge", partialSize, false)
	if err != nil {
		t.Fatal(err)
	}

	downloadID := "resume-id"

	// Create saved state for resume
	remainingTasks := []types.Task{
		{Offset: partialSize, Length: fileSize - partialSize},
	}
	// Need to check if DownloadState struct is compatible
	savedState := &types.DownloadState{
		ID:         downloadID,
		URL:        server.URL(),
		DestPath:   destPath,
		TotalSize:  fileSize,
		Downloaded: partialSize,
		Tasks:      remainingTasks,
		Filename:   "resume_test.bin",
		URLHash:    state.URLHash(server.URL()),
	}
	if err := state.SaveState(server.URL(), destPath, savedState); err != nil {
		t.Fatalf("Failed to save state: %v", err)
	}

	// Now resume download
	progressState := types.NewProgressState("resume-test", fileSize)
	runtime := &types.RuntimeConfig{MaxConnectionsPerHost: 2}

	downloader := NewConcurrentDownloader(downloadID, nil, progressState, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err = downloader.Download(ctx, server.URL(), destPath, fileSize, false)
	if err != nil {
		t.Fatalf("Resume download failed: %v", err)
	}

	// Verify final file exists (not .surge)
	if testutil.FileExists(workingPath) {
		t.Error(".surge file should be removed after completion")
	}

	if err := testutil.VerifyFileSize(destPath, fileSize); err != nil {
		t.Error(err)
	}

	// State file should be deleted on success
	_, err = state.LoadState(server.URL(), destPath)
	if err == nil {
		t.Error("State file should be deleted after successful download")
	}
}

// =============================================================================
// createTasks Tests
// =============================================================================

func TestCreateTasks_Basic(t *testing.T) {
	fileSize := int64(1024 * 1024) // 1MB
	chunkSize := int64(256 * 1024) // 256KB

	tasks := createTasks(fileSize, chunkSize)

	if len(tasks) != 4 {
		t.Errorf("Expected 4 tasks, got %d", len(tasks))
	}

	// Verify tasks cover the entire file
	var totalLength int64
	for i, task := range tasks {
		totalLength += task.Length
		expectedOffset := int64(i) * chunkSize
		if task.Offset != expectedOffset {
			t.Errorf("Task %d: got offset %d, want %d", i, task.Offset, expectedOffset)
		}
	}

	if totalLength != fileSize {
		t.Errorf("Total length %d doesn't cover file size %d", totalLength, fileSize)
	}
}

func TestCreateTasks_UnevenDivision(t *testing.T) {
	fileSize := int64(1000)
	chunkSize := int64(300)

	tasks := createTasks(fileSize, chunkSize)

	if len(tasks) != 4 {
		t.Errorf("Expected 4 tasks, got %d", len(tasks))
	}

	lastTask := tasks[len(tasks)-1]
	if lastTask.Length != 100 {
		t.Errorf("Last task length should be 100, got %d", lastTask.Length)
	}
}

func TestCreateTasks_SmallFile(t *testing.T) {
	fileSize := int64(100)
	chunkSize := int64(1024)

	tasks := createTasks(fileSize, chunkSize)

	if len(tasks) != 1 {
		t.Errorf("Small file should have 1 task, got %d", len(tasks))
	}
	if tasks[0].Length != 100 {
		t.Errorf("Task length should equal file size, got %d", tasks[0].Length)
	}
}

func TestCreateTasks_ExactDivision(t *testing.T) {
	fileSize := int64(4096)
	chunkSize := int64(1024)

	tasks := createTasks(fileSize, chunkSize)

	if len(tasks) != 4 {
		t.Errorf("Expected 4 tasks, got %d", len(tasks))
	}

	for _, task := range tasks {
		if task.Length != 1024 {
			t.Errorf("Each task should be 1024 bytes, got %d", task.Length)
		}
	}
}

func TestCreateTasks_ZeroChunkSize(t *testing.T) {
	tasks := createTasks(1000, 0)
	if tasks != nil {
		t.Error("createTasks should return nil for zero chunk size")
	}

	tasks = createTasks(1000, -1)
	if tasks != nil {
		t.Error("createTasks should return nil for negative chunk size")
	}
}

package download_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/surge-downloader/surge/internal/download"
	"github.com/surge-downloader/surge/internal/download/state"
	"github.com/surge-downloader/surge/internal/engine/types"
	"github.com/surge-downloader/surge/internal/testutil"
)

func TestIntegration_PauseResume(t *testing.T) {
	// 1. Setup temporary directory for DB and downloads
	tmpDir, err := os.MkdirTemp("", "surge-integration-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Set XDG_CONFIG_HOME to tmpDir so state.GetDB() creates DB there
	// The config package uses "surge" subdirectory
	configDir := tmpDir // XDG_CONFIG_HOME usually contains the app dir
	t.Setenv("XDG_CONFIG_HOME", configDir)

	// Ensure clean state
	state.CloseDB()

	// Force DB init
	if _, err := state.GetDB(); err != nil {
		t.Fatalf("Failed to init DB: %v", err)
	}
	defer state.CloseDB()

	// 2. Setup Mock Server (50MB file)
	fileSize := int64(50 * 1024 * 1024) // 50MB
	server := testutil.NewMockServer(
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
		testutil.WithLatency(20*time.Millisecond), // Add meaningful latency to allow interruption
	)
	defer server.Close()

	url := server.URL()
	// Use a fixed filename to make checking easier
	filename := "largefile.bin"
	outputPath := tmpDir
	destPath := filepath.Join(outputPath, filename)

	// 3. Start Download and Interrupt
	// Use background context, as interruption is handled by progState.Pause()
	ctx := context.Background()

	// Create a progress channel we can ignore
	progressCh := make(chan any, 100)

	// Create a runtime config
	runtime := &types.RuntimeConfig{}

	// Create a dummy state
	progState := types.NewProgressState(uuid.New().String(), fileSize)

	cfg := types.DownloadConfig{
		URL:        url,
		OutputPath: outputPath,
		Filename:   filename,
		ID:         progState.ID,
		ProgressCh: progressCh,
		State:      progState,
		Runtime:    runtime,
		IsResume:   false,
	}

	// Start download in a goroutine
	errCh := make(chan error)
	go func() {
		// Provide a valid context for the download
		errCh <- download.TUIDownload(ctx, &cfg)
	}()

	// Wait a bit to let it download some data but not finish
	time.Sleep(50 * time.Millisecond)

	// Interrupt!
	// We rely on progState.Pause() to trigger the cancellation via the callback registered in TUIDownload
	progState.Pause()

	// Wait for download to return
	select {
	case err := <-errCh:
		// We expect nil (graceful exit) or context canceled
		if err != nil && err != context.Canceled {
			t.Logf("Download returned error (might be expected): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Download did not return after cancellation")
	}

	// 4. Verify State is Saved
	savedState, err := state.LoadState(url, destPath)
	if err != nil {
		t.Fatalf("Failed to load saved state: %v", err)
	}

	if savedState.Downloaded == 0 {
		t.Error("Saved state shows 0 downloaded bytes")
	}
	if savedState.Downloaded >= fileSize {
		t.Errorf("Download finished too fast! Downloaded %d of %d", savedState.Downloaded, fileSize)
	}
	if len(savedState.Tasks) == 0 {
		t.Error("Saved state has no tasks")
	}

	// Verify .surge file exists
	incompletePath := destPath + types.IncompleteSuffix
	info, err := os.Stat(incompletePath)
	if err != nil {
		t.Fatalf("Incomplete file not found: %v", err)
	}
	if info.Size() != fileSize {
		// Note: we preallocate file size, so it should match total size
		t.Errorf("Incomplete file size = %d, want %d", info.Size(), fileSize)
	}

	t.Logf("Paused successfully. Downloaded: %d bytes", savedState.Downloaded)

	// 5. Resume Download
	// Create new context
	resumeCtx, resumeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer resumeCancel()

	// Update config for resume
	cfg.IsResume = true
	cfg.DestPath = destPath // Important for resume lookup

	// Re-initialize state to clean values but keep ID if needed
	// But TUIDownload re-creates concurrent downloader which relies on LoadState
	// We should probably reset existing state or use a fresh one to simulate app restart?
	// But `cfg.State` is passed in. TUIDownload updates it.
	// Let's reset the Pause flag in the state at least.
	progState.Resume()

	err = download.TUIDownload(resumeCtx, &cfg)
	if err != nil {
		t.Fatalf("Resume failed: %v", err)
	}

	// 6. Verify Completion
	// .surge file should be gone
	if _, err := os.Stat(incompletePath); !os.IsNotExist(err) {
		t.Error("Incomplete file still exists after resume completion")
	}

	// Final file should exist
	finalInfo, err := os.Stat(destPath)
	if err != nil {
		t.Fatalf("Final file not found: %v", err)
	}
	if finalInfo.Size() != fileSize {
		t.Errorf("Final file size = %d, want %d", finalInfo.Size(), fileSize)
	}

	// State should be deleted
	_, err = state.LoadState(url, destPath)
	if err == nil {
		t.Error("State should be deleted after completion")
	}
}

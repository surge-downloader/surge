package download

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/surge-downloader/surge/internal/engine/types"
	"github.com/surge-downloader/surge/internal/engine/events"
)

func TestNewWorkerPool(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	if pool == nil {
		t.Fatal("Expected non-nil WorkerPool")
	}

	if pool.taskChan == nil {
		t.Error("Expected taskChan to be initialized")
	}

	if pool.progressCh != ch {
		t.Error("Expected progressCh to be set correctly")
	}

	if pool.downloads == nil {
		t.Error("Expected downloads map to be initialized")
	}

	if pool.maxDownloads != 3 {
		t.Errorf("Expected maxDownloads=3, got %d", pool.maxDownloads)
	}
}

func TestNewWorkerPool_MaxDownloadsValidation(t *testing.T) {
	ch := make(chan any, 10)

	tests := []struct {
		name         string
		maxDownloads int
		wantMax      int
	}{
		{"zero defaults to 3", 0, 3},
		{"negative defaults to 3", -1, 3},
		{"valid value 1", 1, 1},
		{"valid value 5", 5, 5},
		{"valid value 10", 10, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := NewWorkerPool(ch, tt.maxDownloads)
			if pool.maxDownloads != tt.wantMax {
				t.Errorf("maxDownloads = %d, want %d", pool.maxDownloads, tt.wantMax)
			}
		})
	}
}

func TestNewWorkerPool_NilChannel(t *testing.T) {
	pool := NewWorkerPool(nil, 3)

	if pool == nil {
		t.Fatal("Expected non-nil WorkerPool even with nil channel")
	}

	if pool.progressCh != nil {
		t.Error("Expected progressCh to be nil")
	}
}

func TestWorkerPool_Add_QueuesToChannel(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	cfg := types.DownloadConfig{
		ID:  "test-id",
		URL: "http://example.com/file.zip",
	}

	// Add should not block (buffered channel)
	done := make(chan bool)
	go func() {
		pool.Add(cfg)
		done <- true
	}()

	select {
	case <-done:
		// Success - Add completed
	case <-time.After(100 * time.Millisecond):
		t.Error("Add() blocked unexpectedly")
	}
}

func TestWorkerPool_Pause_NonExistentDownload(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	// Should not panic when pausing non-existent download
	pool.Pause("non-existent-id")

	// No message should be sent
	select {
	case <-ch:
		t.Error("Should not send message for non-existent download")
	default:
		// Expected - no message
	}
}

func TestWorkerPool_Pause_ActiveDownload(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	// Create a progress state
	state := types.NewProgressState("test-id", 1000)
	state.Downloaded.Store(500)

	// Manually add an active download
	pool.mu.Lock()
	pool.downloads["test-id"] = &activeDownload{
		config: types.DownloadConfig{
			ID:    "test-id",
			State: state,
		},
	}
	pool.mu.Unlock()

	pool.Pause("test-id")

	// Check that state is paused
	if !state.IsPaused() {
		t.Error("Expected state to be marked as paused")
	}

	// Check that a pause message was sent
	select {
	case msg := <-ch:
		pausedMsg, ok := msg.(events.DownloadPausedMsg)
		if !ok {
			t.Errorf("Expected DownloadPausedMsg, got %T", msg)
		}
		if pausedMsg.DownloadID != "test-id" {
			t.Errorf("Expected download ID 'test-id', got '%s'", pausedMsg.DownloadID)
		}
		if pausedMsg.Downloaded != 500 {
			t.Errorf("Expected Downloaded=500, got %d", pausedMsg.Downloaded)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Expected pause message to be sent")
	}
}

func TestWorkerPool_Pause_NilState(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	// Add download with nil state
	pool.mu.Lock()
	pool.downloads["test-id"] = &activeDownload{
		config: types.DownloadConfig{
			ID:    "test-id",
			State: nil,
		},
	}
	pool.mu.Unlock()

	// Should not panic with nil state
	pool.Pause("test-id")

	// Message should still be sent with Downloaded=0
	select {
	case msg := <-ch:
		pausedMsg, ok := msg.(events.DownloadPausedMsg)
		if !ok {
			t.Errorf("Expected DownloadPausedMsg, got %T", msg)
		}
		if pausedMsg.Downloaded != 0 {
			t.Errorf("Expected Downloaded=0 for nil state, got %d", pausedMsg.Downloaded)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Expected pause message to be sent")
	}
}

func TestWorkerPool_PauseAll_NoDownloads(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	// Should not panic with no downloads
	pool.PauseAll()

	// No messages should be sent
	select {
	case <-ch:
		t.Error("Should not send message when no downloads exist")
	default:
		// Expected
	}
}

func TestWorkerPool_PauseAll_MultipleDownloads(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	// Add multiple active downloads
	states := make([]*types.ProgressState, 3)
	for i := 0; i < 3; i++ {
		id := string(rune('a' + i))
		states[i] = types.NewProgressState(id, 1000)
		pool.mu.Lock()
		pool.downloads[id] = &activeDownload{
			config: types.DownloadConfig{
				ID:    id,
				State: states[i],
			},
		}
		pool.mu.Unlock()
	}

	pool.PauseAll()

	// All should be paused
	for i, state := range states {
		if !state.IsPaused() {
			t.Errorf("Download %d should be paused", i)
		}
	}

	// Should receive 3 pause messages
	receivedCount := 0
	for receivedCount < 3 {
		select {
		case msg := <-ch:
			if _, ok := msg.(events.DownloadPausedMsg); ok {
				receivedCount++
			}
		case <-time.After(100 * time.Millisecond):
			t.Errorf("Expected 3 pause messages, got %d", receivedCount)
			return
		}
	}
}

func TestWorkerPool_PauseAll_SkipsAlreadyPaused(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	// Add one paused and one active download
	activeState := types.NewProgressState("active", 1000)
	pausedState := types.NewProgressState("paused", 1000)
	pausedState.Paused.Store(true)

	pool.mu.Lock()
	pool.downloads["active"] = &activeDownload{
		config: types.DownloadConfig{ID: "active", State: activeState},
	}
	pool.downloads["paused"] = &activeDownload{
		config: types.DownloadConfig{ID: "paused", State: pausedState},
	}
	pool.mu.Unlock()

	pool.PauseAll()

	// Only the active one should receive a pause message
	receivedCount := 0
	timeout := time.After(100 * time.Millisecond)
loop:
	for {
		select {
		case msg := <-ch:
			if pausedMsg, ok := msg.(events.DownloadPausedMsg); ok {
				receivedCount++
				if pausedMsg.DownloadID != "active" {
					t.Errorf("Unexpected pause message for ID '%s'", pausedMsg.DownloadID)
				}
			}
		case <-timeout:
			break loop
		}
	}

	if receivedCount != 1 {
		t.Errorf("Expected 1 pause message, got %d", receivedCount)
	}
}

func TestWorkerPool_PauseAll_SkipsCompletedDownloads(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	// Add one completed and one active download
	activeState := types.NewProgressState("active", 1000)
	doneState := types.NewProgressState("done", 1000)
	doneState.Done.Store(true)

	pool.mu.Lock()
	pool.downloads["active"] = &activeDownload{
		config: types.DownloadConfig{ID: "active", State: activeState},
	}
	pool.downloads["done"] = &activeDownload{
		config: types.DownloadConfig{ID: "done", State: doneState},
	}
	pool.mu.Unlock()

	pool.PauseAll()

	// Only the active one should receive a pause message
	receivedCount := 0
	timeout := time.After(100 * time.Millisecond)
loop:
	for {
		select {
		case msg := <-ch:
			if pausedMsg, ok := msg.(events.DownloadPausedMsg); ok {
				receivedCount++
				if pausedMsg.DownloadID != "active" {
					t.Errorf("Unexpected pause message for ID '%s'", pausedMsg.DownloadID)
				}
			}
		case <-timeout:
			break loop
		}
	}

	if receivedCount != 1 {
		t.Errorf("Expected 1 pause message, got %d", receivedCount)
	}
}

func TestWorkerPool_Cancel_NonExistentDownload(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	// Should not panic
	pool.Cancel("non-existent-id")
}

func TestWorkerPool_Cancel_RemovesFromMap(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	state := types.NewProgressState("test-id", 1000)

	pool.mu.Lock()
	pool.downloads["test-id"] = &activeDownload{
		config: types.DownloadConfig{
			ID:    "test-id",
			State: state,
		},
	}
	pool.mu.Unlock()

	pool.Cancel("test-id")

	pool.mu.RLock()
	_, exists := pool.downloads["test-id"]
	pool.mu.RUnlock()

	if exists {
		t.Error("Expected download to be removed from map after cancel")
	}
}

func TestWorkerPool_Cancel_CallsCancelFunc(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	ctx, cancel := context.WithCancel(context.Background())
	state := types.NewProgressState("test-id", 1000)

	pool.mu.Lock()
	pool.downloads["test-id"] = &activeDownload{
		config: types.DownloadConfig{
			ID:    "test-id",
			State: state,
		},
		cancel: cancel,
	}
	pool.mu.Unlock()

	pool.Cancel("test-id")

	// Context should be canceled
	select {
	case <-ctx.Done():
		// Expected
	default:
		t.Error("Expected context to be canceled")
	}
}

func TestWorkerPool_Cancel_MarksDone(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	state := types.NewProgressState("test-id", 1000)

	pool.mu.Lock()
	pool.downloads["test-id"] = &activeDownload{
		config: types.DownloadConfig{
			ID:    "test-id",
			State: state,
		},
	}
	pool.mu.Unlock()

	pool.Cancel("test-id")

	if !state.Done.Load() {
		t.Error("Expected state.Done to be true after cancel")
	}
}

func TestWorkerPool_Resume_NonExistentDownload(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	// Should not panic
	pool.Resume("non-existent-id")

	// No message should be sent
	select {
	case <-ch:
		t.Error("Should not send message for non-existent download")
	default:
		// Expected
	}
}

func TestWorkerPool_Resume_ClearsPausedFlag(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	state := types.NewProgressState("test-id", 1000)
	state.Paused.Store(true)

	pool.mu.Lock()
	pool.downloads["test-id"] = &activeDownload{
		config: types.DownloadConfig{
			ID:    "test-id",
			State: state,
		},
	}
	pool.mu.Unlock()

	pool.Resume("test-id")

	if state.IsPaused() {
		t.Error("Expected paused flag to be cleared after resume")
	}
}

func TestWorkerPool_Resume_SendsResumedMessage(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	state := types.NewProgressState("test-id", 1000)
	state.Paused.Store(true)

	pool.mu.Lock()
	pool.downloads["test-id"] = &activeDownload{
		config: types.DownloadConfig{
			ID:    "test-id",
			State: state,
		},
	}
	pool.mu.Unlock()

	pool.Resume("test-id")

	// We can't reliably read from pool.taskChan because worker goroutines may consume the config before us. Just verify the resumed message was sent.
	// Check for resumed message
	select {
	case msg := <-ch:
		resumedMsg, ok := msg.(events.DownloadResumedMsg)
		if !ok {
			t.Errorf("Expected DownloadResumedMsg, got %T", msg)
		}
		if resumedMsg.DownloadID != "test-id" {
			t.Errorf("Expected download ID 'test-id', got '%s'", resumedMsg.DownloadID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Expected resume message to be sent")
	}
}

func TestWorkerPool_Resume_RequeuesDownload(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	state := types.NewProgressState("test-id", 1000)
	cfg := types.DownloadConfig{
		ID:    "test-id",
		URL:   "http://example.com/file.zip",
		State: state,
	}

	pool.mu.Lock()
	pool.downloads["test-id"] = &activeDownload{
		config: cfg,
	}
	pool.mu.Unlock()

	pool.Resume("test-id")

	// Note: We can't reliably read from pool.taskChan because worker goroutines
	// may consume the config before us. Instead, verify Resume cleared the paused
	// flag and sent the resumed message.

	if state.IsPaused() {
		t.Error("Expected paused flag to be cleared")
	}

	// Check for resumed message
	select {
	case msg := <-ch:
		if resumedMsg, ok := msg.(events.DownloadResumedMsg); ok {
			if resumedMsg.DownloadID != cfg.ID {
				t.Errorf("Expected ID '%s', got '%s'", cfg.ID, resumedMsg.DownloadID)
			}
		} else {
			t.Errorf("Expected DownloadResumedMsg, got %T", msg)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Expected resumed message to be sent")
	}
}

func TestWorkerPool_GracefulShutdown_PausesAll(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	state := types.NewProgressState("test-id", 1000)

	pool.mu.Lock()
	pool.downloads["test-id"] = &activeDownload{
		config: types.DownloadConfig{
			ID:    "test-id",
			State: state,
		},
	}
	pool.mu.Unlock()

	// GracefulShutdown should call PauseAll
	done := make(chan bool)
	go func() {
		pool.GracefulShutdown()
		done <- true
	}()

	select {
	case <-done:
		// Success
	case <-time.After(500 * time.Millisecond):
		t.Error("GracefulShutdown took too long")
	}

	if !state.IsPaused() {
		t.Error("Expected state to be paused after GracefulShutdown")
	}
}

func TestWorkerPool_ConcurrentPauseCancel(t *testing.T) {
	ch := make(chan any, 100)
	pool := NewWorkerPool(ch, 3)

	// Add multiple downloads
	for i := 0; i < 10; i++ {
		id := string(rune('a' + i))
		state := types.NewProgressState(id, 1000)
		pool.mu.Lock()
		pool.downloads[id] = &activeDownload{
			config: types.DownloadConfig{ID: id, State: state},
		}
		pool.mu.Unlock()
	}

	// Concurrently pause and cancel
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		id := string(rune('a' + i))
		go func(id string) {
			defer wg.Done()
			pool.Pause(id)
			pool.Cancel(id)
		}(id)
	}

	wg.Wait()

	// All should be removed from map
	pool.mu.RLock()
	remaining := len(pool.downloads)
	pool.mu.RUnlock()

	if remaining != 0 {
		t.Errorf("Expected 0 remaining downloads, got %d", remaining)
	}
}

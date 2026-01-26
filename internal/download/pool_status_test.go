package download

import (
	"testing"

	"github.com/surge-downloader/surge/internal/engine/types"
)

func TestWorkerPool_GetStatus_NonExistent(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	status := pool.GetStatus("non-existent-id")
	if status != nil {
		t.Error("Expected nil status for non-existent download")
	}
}

func TestWorkerPool_GetStatus_Active(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	id := "test-id"
	state := types.NewProgressState(id, 1000)
	state.Downloaded.Store(500)

	pool.mu.Lock()
	pool.downloads[id] = &activeDownload{
		config: types.DownloadConfig{
			ID:       id,
			URL:      "http://example.com/file",
			Filename: "file",
			State:    state,
		},
	}
	pool.mu.Unlock()

	status := pool.GetStatus(id)
	if status == nil {
		t.Fatal("Expected status to be returned")
	}

	if status.ID != id {
		t.Errorf("Expected ID %s, got %s", id, status.ID)
	}
	if status.Status != "downloading" {
		t.Errorf("Expected status 'downloading', got '%s'", status.Status)
	}
	if status.TotalSize != 1000 {
		t.Errorf("Expected TotalSize 1000, got %d", status.TotalSize)
	}
	if status.Downloaded != 500 {
		t.Errorf("Expected Downloaded 500, got %d", status.Downloaded)
	}
	if status.Progress != 50.0 {
		t.Errorf("Expected Progress 50.0, got %.1f", status.Progress)
	}
}

func TestWorkerPool_GetStatus_Paused(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	id := "test-id"
	state := types.NewProgressState(id, 1000)
	state.Pause()

	pool.mu.Lock()
	pool.downloads[id] = &activeDownload{
		config: types.DownloadConfig{ID: id, State: state},
	}
	pool.mu.Unlock()

	status := pool.GetStatus(id)
	if status == nil {
		t.Fatal("Expected status to be returned")
	}

	if status.Status != "paused" {
		t.Errorf("Expected status 'paused', got '%s'", status.Status)
	}
}

func TestWorkerPool_GetStatus_Completed(t *testing.T) {
	ch := make(chan any, 10)
	pool := NewWorkerPool(ch, 3)

	id := "test-id"
	state := types.NewProgressState(id, 1000)
	state.Done.Store(true)

	pool.mu.Lock()
	pool.downloads[id] = &activeDownload{
		config: types.DownloadConfig{ID: id, State: state},
	}
	pool.mu.Unlock()

	status := pool.GetStatus(id)
	if status == nil {
		t.Fatal("Expected status to be returned")
	}

	if status.Status != "completed" {
		t.Errorf("Expected status 'completed', got '%s'", status.Status)
	}
}

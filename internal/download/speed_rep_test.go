package download

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/surge-downloader/surge/internal/download/types"

	tea "github.com/charmbracelet/bubbletea"
)

func TestGetStatusSpeed(t *testing.T) {
	// Create a dummy progress channel
	progressCh := make(chan tea.Msg, 100)
	pool := NewWorkerPool(progressCh, 1)

	// Create a dummy download config
	id := "test-speed-id"
	state := types.NewProgressState(id, 1024*1024*10) // 10MB

	// Set initial total size (resets StartTime to Now)
	state.SetTotalSize(1024 * 1024 * 10)

	// Artificially modify the StartTime to be 2 seconds ago.
	rs := reflect.ValueOf(state).Elem()
	startTimeField := rs.FieldByName("StartTime")
	// modifying the field
	twoSecondsAgo := time.Now().Add(-2 * time.Second)

	// Set directly via reflection
	startTimeField.Set(reflect.ValueOf(twoSecondsAgo))

	cfg := types.DownloadConfig{
		ID:         id,
		URL:        "http://example.com/file",
		Filename:   "file",
		OutputPath: ".",
		State:      state,
	}

	pool.mu.Lock()
	pool.downloads[id] = &activeDownload{
		config: cfg,
	}
	pool.mu.Unlock()

	// Simulate 5MB downloaded
	state.Downloaded.Store(5 * 1024 * 1024)

	// Get Status
	status := pool.GetStatus(id)

	if status == nil {
		t.Fatal("Status is nil")
	}

	// 5MB / 2s = 2.5 MB/s
	// We expect Speed to be approx 2.5

	// Currently it should be 0 because it's not calculated.
	fmt.Printf("Speed: %f\n", status.Speed)

	if status.Speed > 0 {
		t.Logf("Success: speed is %f MB/s", status.Speed/1024/1024)
	} else {
		t.Errorf("speed is 0 after fix, got %f", status.Speed)
	}
}

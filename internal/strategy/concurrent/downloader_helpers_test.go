package concurrent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/SurgeDM/Surge/internal/progress"
	"github.com/SurgeDM/Surge/internal/store"
	"github.com/SurgeDM/Surge/internal/testutil"
	"github.com/SurgeDM/Surge/internal/types"
)

func TestHandlePause_Normal(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	cleanup := func() {}
	defer cleanup()

	fileSize := int64(1000)
	destPath := filepath.Join(tmpDir, "test.bin")
	state := progress.New("test-id", fileSize)
	downloader := &ConcurrentDownloader{
		ID:      "test-id",
		State:   state,
		Runtime: &types.RuntimeConfig{},
	}

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 500, Length: 500})

	err := downloader.handlePause(destPath, fileSize, queue, nil)
	if !errors.Is(err, types.ErrPaused) {
		t.Fatalf("Expected ErrPaused, got %v", err)
	}
}

func TestHandlePause_PersistsWithoutLifecycleConsumer(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "test.bin")
	state := progress.New("test-id", 1000)
	progressCh := make(chan types.DownloadEvent, 1)
	progressCh <- types.DownloadEvent{}
	download := &ConcurrentDownloader{
		ID:           "test-id",
		URL:          "http://example.com/file.bin",
		State:        state,
		ProgressChan: progressCh,
		Runtime:      &types.RuntimeConfig{},
	}
	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 500, Length: 500})

	if err := download.handlePause(destPath, 1000, queue, nil); !errors.Is(err, types.ErrPaused) {
		t.Fatalf("handlePause = %v, want ErrPaused", err)
	}
	if snapshot := state.TakePendingResumeState(); snapshot == nil {
		t.Fatal("pause state was not retained")
	}
}

func TestHandlePause_UsesLiveRateLimitFromState(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	cleanup := func() {}
	defer cleanup()

	fileSize := int64(1000)
	destPath := filepath.Join(tmpDir, "test.bin")
	state := progress.New("test-id", fileSize)
	state.SetRateLimit(3*1024*1024, true)
	progressCh := make(chan types.DownloadEvent, 1)
	downloader := &ConcurrentDownloader{
		ID:           "test-id",
		URL:          "http://example.com/file.bin",
		State:        state,
		ProgressChan: progressCh,
		Runtime:      &types.RuntimeConfig{},
		RateLimitBps: 1,
		RateLimitSet: false,
	}

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 500, Length: 500})

	err := downloader.handlePause(destPath, fileSize, queue, nil)
	if !errors.Is(err, types.ErrPaused) {
		t.Fatalf("Expected ErrPaused, got %v", err)
	}

	msg, ok := <-progressCh
	if !ok {
		t.Fatalf("expected DownloadPausedMsg, got %T", msg)
	}
	if msg.RateLimit != 3*1024*1024 || !msg.RateLimitSet {
		t.Fatalf("pause msg rate limit = (%d, %v), want (%d, true)", msg.RateLimit, msg.RateLimitSet, 3*1024*1024)
	}
	if msg.State == nil {
		t.Fatal("expected pause state")
	}
	if pending := state.TakePendingResumeState(); pending != nil {
		t.Fatalf("delivered pause event left fallback state pending: %+v", pending)
	}
}

func TestSetupTasks_NewDownload(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	cleanup := func() {}
	defer cleanup()

	fileSize := int64(1000)
	chunkSize := int64(500)
	destPath := filepath.Join(tmpDir, "new.bin")
	workingPath := destPath + types.IncompleteSuffix

	f, err := os.Create(workingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	state := progress.New("test-id", fileSize)
	downloader := &ConcurrentDownloader{
		ID:      "test-id",
		State:   state,
		Runtime: &types.RuntimeConfig{},
	}

	tasks, err := downloader.setupTasks(destPath, fileSize, chunkSize, 2, f, nil, false)
	if err != nil {
		t.Fatalf("setupTasks failed: %v", err)
	}

	if len(tasks) != 2 {
		t.Errorf("Expected 2 tasks, got %d", len(tasks))
	}
}

func TestGetWorkerMirrors(t *testing.T) {
	d := &ConcurrentDownloader{URL: "http://primary.com"}
	active := []string{"http://primary.com", "http://mirror1.com", "http://mirror2.com"}

	mirrors := d.getWorkerMirrors(active)

	if len(mirrors) != 3 {
		t.Errorf("Expected 3 mirrors, got %d", len(mirrors))
	}
	if mirrors[0] != "http://primary.com" {
		t.Errorf("Primary URL should be first, got %s", mirrors[0])
	}
}

func TestInitMirrorStatus(t *testing.T) {
	state := progress.New("test-id", 1000)
	d := &ConcurrentDownloader{ID: "test-id", State: state}

	primary := "http://primary.com"
	candidates := []string{"http://mirror1.com", "http://mirror2.com"}
	active := []string{"http://primary.com", "http://mirror1.com"}

	d.initMirrorStatus(primary, candidates, active, "/path/to/dest")

	statuses := state.GetMirrors()
	if len(statuses) != 3 {
		t.Errorf("Expected 3 statuses, got %d", len(statuses))
	}

	foundMirror2 := false
	for _, s := range statuses {
		if s.URL == "http://mirror2.com" {
			foundMirror2 = true
			if s.Active {
				t.Error("Mirror2 should be inactive")
			}
			if !s.Error {
				t.Error("Mirror2 should have error (as it is not active)")
			}
		}
	}
	if !foundMirror2 {
		t.Error("Mirror2 status not found")
	}
}

func TestSetupTasks_BitmapRestoration(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	cleanup := func() {}
	defer cleanup()

	fileSize := int64(1000)
	chunkSize := int64(100)
	destPath := filepath.Join(tmpDir, "resume.bin")

	// Create a saved state
	savedBitmap := []byte{0xFF, 0x00, 0x00} // 10 chunks need 3 bytes
	savedState := &types.DownloadRecord{
		ID:              "test-id",
		URL:             "http://example.com",
		DestPath:        destPath,
		TotalSize:       fileSize,
		Downloaded:      500,
		ActualChunkSize: chunkSize,
		ChunkBitmap:     savedBitmap,
		Tasks:           []types.Task{{Offset: 500, Length: 500}},
	}
	if err := store.AddToMasterList(types.DownloadRecord{
		ID:         "test-id",
		URL:        "http://example.com",
		DestPath:   destPath,
		Filename:   filepath.Base(destPath),
		Status:     "paused",
		TotalSize:  fileSize,
		Downloaded: 500,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState("http://example.com", destPath, savedState); err != nil {
		t.Fatal(err)
	}

	f, _ := os.Create(destPath + types.IncompleteSuffix)
	defer func() { _ = f.Close() }()

	progState := progress.New("test-id", fileSize)
	downloader := &ConcurrentDownloader{
		ID:    "test-id",
		URL:   "http://example.com",
		State: progState,
	}

	// This simulates the fixed order in Download():
	// 1. InitBitmap
	progState.InitBitmap(fileSize, chunkSize)
	// 2. setupTasks (which calls RestoreBitmap)
	_, err := downloader.setupTasks(destPath, fileSize, chunkSize, 1, f, savedState, true)
	if err != nil {
		t.Fatal(err)
	}

	// Verify bitmap is NOT empty (it should have the restored data)
	bitmap, _, _, _, _ := progState.GetBitmapSnapshot(false)
	if len(bitmap) == 0 {
		t.Error("Bitmap should have been restored, but it is empty")
	}
	if bitmap[0] != 0xAA {
		t.Errorf("Bitmap[0] should be 0xAA (all chunks completed), got 0x%02X", bitmap[0])
	}
}

func TestSaveStateSnapshot_PersistsActiveAndQueuedTasks(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	cleanup := func() {}
	defer cleanup()

	fileSize := int64(1000)
	destPath := filepath.Join(tmpDir, "snapshot.bin")
	state := progress.New("test-id", fileSize)
	downloader := &ConcurrentDownloader{
		ID:          "test-id",
		State:       state,
		Runtime:     &types.RuntimeConfig{},
		URL:         "http://example.com/snapshot.bin",
		activeTasks: make(map[int]*ActiveTask),
	}

	if err := store.AddToMasterList(types.DownloadRecord{
		ID:        "test-id",
		URL:       "http://example.com/snapshot.bin",
		DestPath:  destPath,
		Status:    "downloading",
		TotalSize: fileSize,
	}); err != nil {
		t.Fatal(err)
	}

	queue := NewTaskQueue()

	queue.Push(types.Task{Offset: 500, Length: 500})

	// Simulate an active worker that has advanced to 600
	active := &ActiveTask{}
	active.CurrentOffset.Store(600)
	active.StopAt.Store(1000)

	downloader.activeTasks[0] = active

	// Run saveStateSnapshot directly (false to not emit EventPaused, just write to store)
	err := downloader.saveStateSnapshot(destPath, fileSize, queue, nil, false)
	if err != nil {
		t.Fatalf("saveStateSnapshot failed: %v", err)
	}

	// Load the saved state to verify
	saved, err := store.LoadState(downloader.URL, destPath)
	if err != nil {
		t.Fatalf("failed to load saved state: %v", err)
	}

	if len(saved.Tasks) != 2 {
		t.Fatalf("Expected active and queued tasks, got %d", len(saved.Tasks))
	}
	if saved.Tasks[0].Offset != 600 || saved.Tasks[0].Length != 400 {
		t.Errorf("active remaining task = %+v, want offset=600 length=400", saved.Tasks[0])
	}

}

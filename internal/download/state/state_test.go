package state

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/surge-downloader/surge/internal/engine/types"
)

func setupTestDB(t *testing.T) string {
	// Create temp directory for test
	tempDir, err := os.MkdirTemp("", "surge-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Mock environment to point to temp dir
	// We need to set XDG_CONFIG_HOME for Linux, and potentially others for cross-platform safety in tests
	t.Setenv("XDG_CONFIG_HOME", tempDir)
	t.Setenv("APPDATA", tempDir)
	t.Setenv("HOME", tempDir)

	// Reset DB singleton
	dbMu.Lock()
	if db != nil {
		db.Close()
		db = nil
	}
	dbMu.Unlock()

	// Initialize DB
	if err := initDB(); err != nil {
		t.Fatalf("Failed to init DB: %v", err)
	}

	return tempDir
}

func TestURLHash(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantLen int
	}{
		{"simple URL", "https://example.com/file.zip", 16},
		{"URL with path", "https://example.com/path/to/file.zip", 16},
		{"URL with query", "https://example.com/file.zip?token=abc", 16},
		{"different domain", "https://other.org/download", 16},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash := URLHash(tt.url)
			if len(hash) != tt.wantLen {
				t.Errorf("URLHash(%s) length = %d, want %d", tt.url, len(hash), tt.wantLen)
			}
		})
	}
}

func TestURLHashUniqueness(t *testing.T) {
	url1 := "https://example.com/file1.zip"
	url2 := "https://example.com/file2.zip"

	hash1 := URLHash(url1)
	hash2 := URLHash(url2)

	if hash1 == hash2 {
		t.Errorf("Different URLs produced same hash: %s", hash1)
	}
}

func TestSaveLoadState(t *testing.T) {
	tmpDir := setupTestDB(t)
	defer os.RemoveAll(tmpDir)
	defer CloseDB()

	testURL := "https://test.example.com/save-load-test.zip"
	testDestPath := filepath.Join(tmpDir, "testfile.zip")

	id := uuid.New().String()
	originalState := &types.DownloadState{
		ID:         id,
		URL:        testURL,
		DestPath:   testDestPath,
		TotalSize:  1000000,
		Downloaded: 500000,
		Tasks: []types.Task{
			{Offset: 500000, Length: 250000},
			{Offset: 750000, Length: 250000},
		},
		Filename: "save-load-test.zip",
	}

	// Save state
	if err := SaveState(testURL, testDestPath, originalState); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}

	// Load state
	loadedState, err := LoadState(testURL, testDestPath)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}

	// Verify fields
	if loadedState.ID != originalState.ID {
		t.Errorf("ID = %s, want %s", loadedState.ID, originalState.ID)
	}
	if loadedState.URL != originalState.URL {
		t.Errorf("URL = %s, want %s", loadedState.URL, originalState.URL)
	}
	if loadedState.Downloaded != originalState.Downloaded {
		t.Errorf("Downloaded = %d, want %d", loadedState.Downloaded, originalState.Downloaded)
	}
	if loadedState.TotalSize != originalState.TotalSize {
		t.Errorf("TotalSize = %d, want %d", loadedState.TotalSize, originalState.TotalSize)
	}
	if len(loadedState.Tasks) != len(originalState.Tasks) {
		t.Errorf("Tasks count = %d, want %d", len(loadedState.Tasks), len(originalState.Tasks))
	}
	if loadedState.Filename != originalState.Filename {
		t.Errorf("Filename = %s, want %s", loadedState.Filename, originalState.Filename)
	}

	// Verify hashes were set
	if loadedState.URLHash == "" {
		t.Error("URLHash was not set")
	}
}

func TestDeleteState(t *testing.T) {
	tmpDir := setupTestDB(t)
	defer os.RemoveAll(tmpDir)
	defer CloseDB()

	testURL := "https://test.example.com/delete-test.zip"
	testDestPath := filepath.Join(tmpDir, "delete-test.zip")
	id := "test-id-delete"

	state := &types.DownloadState{
		ID:       id,
		URL:      testURL,
		DestPath: testDestPath,
		Filename: "delete-test.zip",
	}

	// Save state
	if err := SaveState(testURL, testDestPath, state); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}

	// Verify it was saved
	if _, err := LoadState(testURL, testDestPath); err != nil {
		t.Fatalf("State was not saved properly: %v", err)
	}

	// Delete state
	if err := DeleteState(id, testURL, testDestPath); err != nil {
		t.Fatalf("DeleteState failed: %v", err)
	}

	// Verify it was deleted
	_, err := LoadState(testURL, testDestPath)
	if err == nil {
		t.Error("LoadState should fail after DeleteState")
	} else if err == sql.ErrNoRows {
		// Acceptable error
	}
}

func TestStateOverwrite(t *testing.T) {
	tmpDir := setupTestDB(t)
	defer os.RemoveAll(tmpDir)
	defer CloseDB()

	testURL := "https://test.example.com/overwrite-test.zip"
	testDestPath := filepath.Join(tmpDir, "overwrite-test.zip")
	id := "test-id-overwrite"

	// First pause at 30%
	state1 := &types.DownloadState{
		ID:         id,
		URL:        testURL,
		DestPath:   testDestPath,
		TotalSize:  1000000,
		Downloaded: 300000, // 30%
		Tasks:      []types.Task{{Offset: 300000, Length: 700000}},
		Filename:   "overwrite-test.zip",
	}
	if err := SaveState(testURL, testDestPath, state1); err != nil {
		t.Fatalf("First SaveState failed: %v", err)
	}

	// Second pause at 80% (simulating resume + more downloading)
	state2 := &types.DownloadState{
		ID:         id,
		URL:        testURL,
		DestPath:   testDestPath,
		TotalSize:  1000000,
		Downloaded: 800000, // 80%
		Tasks:      []types.Task{{Offset: 800000, Length: 200000}},
		Filename:   "overwrite-test.zip",
	}
	if err := SaveState(testURL, testDestPath, state2); err != nil {
		t.Fatalf("Second SaveState failed: %v", err)
	}

	// Load and verify it's 80%, not 30%
	loaded, err := LoadState(testURL, testDestPath)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}

	if loaded.Downloaded != 800000 {
		t.Errorf("Downloaded = %d, want 800000 (state should be overwritten)", loaded.Downloaded)
	}
	if len(loaded.Tasks) != 1 || loaded.Tasks[0].Offset != 800000 {
		t.Errorf("Tasks not properly overwritten, got offset %d", loaded.Tasks[0].Offset)
	}
}

func TestDuplicateURLStateIsolation(t *testing.T) {
	tmpDir := setupTestDB(t)
	defer os.RemoveAll(tmpDir)
	defer CloseDB()

	testURL := "https://example.com/samefile.zip"
	dest1 := filepath.Join(tmpDir, "samefile.zip")
	dest2 := filepath.Join(tmpDir, "samefile(1).zip")
	dest3 := filepath.Join(tmpDir, "samefile(2).zip")

	// Create 3 downloads of the same URL with different destinations
	// IMPORTANT: Must allow separate IDs or rely on unique constraints?
	// The new DB schema has ID as Primary Key.
	// If we don't provide ID, SaveState generates one.

	state1 := &types.DownloadState{
		URL:        testURL,
		DestPath:   dest1,
		TotalSize:  1000000,
		Downloaded: 100000, // 10%
		Tasks:      []types.Task{{Offset: 100000, Length: 900000}},
		Filename:   "samefile.zip",
	}
	state2 := &types.DownloadState{
		URL:        testURL,
		DestPath:   dest2,
		TotalSize:  1000000,
		Downloaded: 500000, // 50%
		Tasks:      []types.Task{{Offset: 500000, Length: 500000}},
		Filename:   "samefile(1).zip",
	}
	state3 := &types.DownloadState{
		URL:        testURL,
		DestPath:   dest3,
		TotalSize:  1000000,
		Downloaded: 900000, // 90%
		Tasks:      []types.Task{{Offset: 900000, Length: 100000}},
		Filename:   "samefile(2).zip",
	}

	// Save all three states
	if err := SaveState(testURL, dest1, state1); err != nil {
		t.Fatalf("SaveState 1 failed: %v", err)
	}
	if err := SaveState(testURL, dest2, state2); err != nil {
		t.Fatalf("SaveState 2 failed: %v", err)
	}
	if err := SaveState(testURL, dest3, state3); err != nil {
		t.Fatalf("SaveState 3 failed: %v", err)
	}

	// Load and verify each has its correct state
	loaded1, err := LoadState(testURL, dest1)
	if err != nil {
		t.Fatalf("LoadState 1 failed: %v", err)
	}
	if loaded1.Downloaded != 100000 {
		t.Errorf("State 1 Downloaded = %d, want 100000", loaded1.Downloaded)
	}
	if loaded1.DestPath != dest1 {
		t.Errorf("State 1 DestPath = %s, want %s", loaded1.DestPath, dest1)
	}

	loaded2, err := LoadState(testURL, dest2)
	if err != nil {
		t.Fatalf("LoadState 2 failed: %v", err)
	}
	if loaded2.Downloaded != 500000 {
		t.Errorf("State 2 Downloaded = %d, want 500000", loaded2.Downloaded)
	}
	if loaded2.DestPath != dest2 {
		t.Errorf("State 2 DestPath = %s, want %s", loaded2.DestPath, dest2)
	}

	loaded3, err := LoadState(testURL, dest3)
	if err != nil {
		t.Fatalf("LoadState 3 failed: %v", err)
	}
	if loaded3.Downloaded != 900000 {
		t.Errorf("State 3 Downloaded = %d, want 900000", loaded3.Downloaded)
	}
	if loaded3.DestPath != dest3 {
		t.Errorf("State 3 DestPath = %s, want %s", loaded3.DestPath, dest3)
	}
}

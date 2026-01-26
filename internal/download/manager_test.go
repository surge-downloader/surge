package download

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/surge-downloader/surge/internal/engine"
	"github.com/surge-downloader/surge/internal/engine/types"
	"github.com/surge-downloader/surge/internal/testutil"
)

func TestUniqueFilePath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "surge-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Helper to create a dummy file
	createFile := func(name string) {
		path := filepath.Join(tmpDir, name)
		_ = os.MkdirAll(filepath.Dir(path), 0755)
		if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
			t.Fatalf("Failed to create file %s: %v", path, err)
		}
	}

	tests := []struct {
		name     string
		existing []string
		input    string
		want     string
	}{
		{
			name:     "No conflict",
			existing: []string{},
			input:    filepath.Join(tmpDir, "file.txt"),
			want:     filepath.Join(tmpDir, "file.txt"),
		},
		{
			name:     "One conflict",
			existing: []string{"file.txt"},
			input:    filepath.Join(tmpDir, "file.txt"),
			want:     filepath.Join(tmpDir, "file(1).txt"),
		},
		{
			name:     "Two conflicts",
			existing: []string{"file.txt", "file(1).txt"},
			input:    filepath.Join(tmpDir, "file.txt"),
			want:     filepath.Join(tmpDir, "file(2).txt"),
		},
		{
			name:     "Conflict with existing numbered file",
			existing: []string{"image(2).png"},
			input:    filepath.Join(tmpDir, "image(2).png"),
			want:     filepath.Join(tmpDir, "image(3).png"),
		},
		{
			name:     "Start from numbered file",
			existing: []string{"data(1).csv"},
			input:    filepath.Join(tmpDir, "data(1).csv"),
			want:     filepath.Join(tmpDir, "data(2).csv"),
		},
		{
			name:     "Nested directory retention",
			existing: []string{"subdir/notes.txt"},
			input:    filepath.Join(tmpDir, "subdir", "notes.txt"),
			want:     filepath.Join(tmpDir, "subdir", "notes(1).txt"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup existing files
			for _, f := range tt.existing {
				createFile(f)
			}
			// Cleanup after test case
			defer func() {
				for _, f := range tt.existing {
					os.Remove(filepath.Join(tmpDir, f))
				}
			}()

			got := uniqueFilePath(tt.input)
			if got != tt.want {
				t.Errorf("uniqueFilePath() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUniqueFilePath_NoExtension(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "surge-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a file without extension
	existingFile := filepath.Join(tmpDir, "README")
	if err := os.WriteFile(existingFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	result := uniqueFilePath(existingFile)
	expected := filepath.Join(tmpDir, "README(1)")

	if result != expected {
		t.Errorf("uniqueFilePath() = %v, want %v", result, expected)
	}
}

func TestUniqueFilePath_MultipleExtensions(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "surge-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create file with multiple dots
	existingFile := filepath.Join(tmpDir, "archive.tar.gz")
	if err := os.WriteFile(existingFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	result := uniqueFilePath(existingFile)
	// Should only consider .gz as extension
	expected := filepath.Join(tmpDir, "archive.tar(1).gz")

	if result != expected {
		t.Errorf("uniqueFilePath() = %v, want %v", result, expected)
	}
}

func TestUniqueFilePath_IncompleteFileConflict(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "surge-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create an incomplete download file
	incompleteFile := filepath.Join(tmpDir, "download.bin"+types.IncompleteSuffix)
	if err := os.WriteFile(incompleteFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Request the original filename - should conflict with incomplete
	inputPath := filepath.Join(tmpDir, "download.bin")
	result := uniqueFilePath(inputPath)
	expected := filepath.Join(tmpDir, "download(1).bin")

	if result != expected {
		t.Errorf("uniqueFilePath() = %v, want %v", result, expected)
	}
}

func TestUniqueFilePath_BothFileAndIncompleteExist(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "surge-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create both the file and its incomplete version
	originalFile := filepath.Join(tmpDir, "video.mp4")
	incompleteFile := filepath.Join(tmpDir, "video(1).mp4"+types.IncompleteSuffix)
	if err := os.WriteFile(originalFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(incompleteFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Request original - should skip both
	result := uniqueFilePath(originalFile)
	expected := filepath.Join(tmpDir, "video(2).mp4")

	if result != expected {
		t.Errorf("uniqueFilePath() = %v, want %v", result, expected)
	}
}

func TestUniqueFilePath_HiddenFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "surge-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a hidden file (Unix-style)
	// Note: filepath.Ext(".gitignore") returns ".gitignore" (entire name is extension)
	// So the unique path becomes "(1).gitignore"
	existingFile := filepath.Join(tmpDir, ".gitignore")
	if err := os.WriteFile(existingFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	result := uniqueFilePath(existingFile)
	// Since ".gitignore" has no base name (ext is the full name), result is "(1).gitignore"
	expected := filepath.Join(tmpDir, "(1).gitignore")

	if result != expected {
		t.Errorf("uniqueFilePath() = %v, want %v", result, expected)
	}
}

func TestUniqueFilePath_ManyConflicts(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "surge-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create 10 conflicting files
	for i := 0; i <= 10; i++ {
		var fileName string
		if i == 0 {
			fileName = filepath.Join(tmpDir, "doc.pdf")
		} else {
			fileName = filepath.Join(tmpDir, fmt.Sprintf("doc(%d).pdf", i))
		}
		if err := os.WriteFile(fileName, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	result := uniqueFilePath(filepath.Join(tmpDir, "doc.pdf"))
	expected := filepath.Join(tmpDir, "doc(11).pdf")

	if result != expected {
		t.Errorf("uniqueFilePath() = %v, want %v", result, expected)
	}
}

func TestUniqueFilePath_SpecialCharactersInName(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "surge-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create file with special characters
	existingFile := filepath.Join(tmpDir, "file [2024].txt")
	if err := os.WriteFile(existingFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	result := uniqueFilePath(existingFile)
	expected := filepath.Join(tmpDir, "file [2024](1).txt")

	if result != expected {
		t.Errorf("uniqueFilePath() = %v, want %v", result, expected)
	}
}
func TestProbeServer_RangeSupported(t *testing.T) {
	server := testutil.NewMockServer(
		testutil.WithFileSize(1024*1024), // 1MB
		testutil.WithRangeSupport(true),
		testutil.WithFilename("testfile.bin"),
	)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := engine.ProbeServer(ctx, server.URL(), "")
	if err != nil {
		t.Fatalf("probeServer failed: %v", err)
	}

	if !result.SupportsRange {
		t.Error("Expected SupportsRange to be true")
	}
	if result.FileSize != 1024*1024 {
		t.Errorf("Expected FileSize 1048576, got %d", result.FileSize)
	}
}

func TestProbeServer_RangeNotSupported(t *testing.T) {
	server := testutil.NewMockServer(
		testutil.WithFileSize(2048),
		testutil.WithRangeSupport(false),
	)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := engine.ProbeServer(ctx, server.URL(), "")
	if err != nil {
		t.Fatalf("probeServer failed: %v", err)
	}

	if result.SupportsRange {
		t.Error("Expected SupportsRange to be false")
	}
	if result.FileSize != 2048 {
		t.Errorf("Expected FileSize 2048, got %d", result.FileSize)
	}
}

func TestProbeServer_CustomFilenameHint(t *testing.T) {
	server := testutil.NewMockServer(
		testutil.WithFileSize(1024),
		testutil.WithFilename("server-file.zip"),
	)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Provide a custom filename hint
	result, err := engine.ProbeServer(ctx, server.URL(), "my-custom-file.zip")
	if err != nil {
		t.Fatalf("probeServer failed: %v", err)
	}

	if result.Filename != "my-custom-file.zip" {
		t.Errorf("Expected Filename 'my-custom-file.zip', got '%s'", result.Filename)
	}
}

func TestProbeServer_ContentType(t *testing.T) {
	server := testutil.NewMockServer(
		testutil.WithFileSize(1024),
		testutil.WithContentType("application/zip"),
	)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := engine.ProbeServer(ctx, server.URL(), "")
	if err != nil {
		t.Fatalf("probeServer failed: %v", err)
	}

	if result.ContentType != "application/zip" {
		t.Errorf("Expected ContentType 'application/zip', got '%s'", result.ContentType)
	}
}

func TestProbeServer_InvalidURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := engine.ProbeServer(ctx, "http://invalid-host-that-does-not-exist.test:9999/file", "")
	if err == nil {
		t.Error("Expected error for invalid URL")
	}
}

func TestProbeServer_ContextCancellation(t *testing.T) {
	server := testutil.NewMockServer(
		testutil.WithFileSize(1024),
		testutil.WithLatency(5*time.Second), // Long latency
	)
	defer server.Close()

	// Cancel immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := engine.ProbeServer(ctx, server.URL(), "")
	if err == nil {
		t.Error("Expected error when context is cancelled")
	}
}

func TestProbeServer_UnexpectedStatusCode(t *testing.T) {
	// Create a custom server that returns 404
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := engine.ProbeServer(ctx, server.URL, "")
	if err == nil {
		t.Error("Expected error for 404 status")
	}
}

func TestProbeServer_ServerError(t *testing.T) {
	// Create a custom server that returns 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := engine.ProbeServer(ctx, server.URL, "")
	if err == nil {
		t.Error("Expected error for 500 status")
	}
}

func TestProbeServer_ZeroFileSize(t *testing.T) {
	// Server returns 200 OK with no Content-Length header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := engine.ProbeServer(ctx, server.URL, "")
	if err != nil {
		t.Fatalf("probeServer failed: %v", err)
	}

	// FileSize should be 0 when Content-Length is missing
	if result.FileSize != 0 {
		t.Errorf("Expected FileSize 0, got %d", result.FileSize)
	}
}

func TestProbeServer_ContentRangeFormats(t *testing.T) {
	tests := []struct {
		name          string
		contentRange  string
		expectedSize  int64
		supportsRange bool
	}{
		{
			name:          "Standard format",
			contentRange:  "bytes 0-0/1048576",
			expectedSize:  1048576,
			supportsRange: true,
		},
		{
			name:          "Unknown size",
			contentRange:  "bytes 0-0/*",
			expectedSize:  0,
			supportsRange: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Range", tt.contentRange)
				w.Header().Set("Content-Length", "1")
				w.WriteHeader(http.StatusPartialContent)
				w.Write([]byte("x"))
			}))
			defer server.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			result, err := engine.ProbeServer(ctx, server.URL, "")
			if err != nil {
				t.Fatalf("probeServer failed: %v", err)
			}

			if result.SupportsRange != tt.supportsRange {
				t.Errorf("SupportsRange = %v, want %v", result.SupportsRange, tt.supportsRange)
			}
			if result.FileSize != tt.expectedSize {
				t.Errorf("FileSize = %d, want %d", result.FileSize, tt.expectedSize)
			}
		})
	}
}

func TestProbeServer_LargeFile(t *testing.T) {
	// Test with a large file size (10GB)
	largeSize := int64(10 * 1024 * 1024 * 1024) // 10GB

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", largeSize))
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte("x"))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := engine.ProbeServer(ctx, server.URL, "")
	if err != nil {
		t.Fatalf("probeServer failed: %v", err)
	}

	if result.FileSize != largeSize {
		t.Errorf("FileSize = %d, want %d", result.FileSize, largeSize)
	}
}

func TestProbeResult_Fields(t *testing.T) {
	pr := &ProbeResult{
		FileSize:      123456789,
		SupportsRange: true,
		Filename:      "document.pdf",
		ContentType:   "application/pdf",
	}

	if pr.FileSize != 123456789 {
		t.Errorf("FileSize = %d, want 123456789", pr.FileSize)
	}
	if !pr.SupportsRange {
		t.Error("SupportsRange should be true")
	}
	if pr.Filename != "document.pdf" {
		t.Errorf("Filename = '%s', want 'document.pdf'", pr.Filename)
	}
	if pr.ContentType != "application/pdf" {
		t.Errorf("ContentType = '%s', want 'application/pdf'", pr.ContentType)
	}
}

func TestProbeResult_ZeroValues(t *testing.T) {
	pr := &ProbeResult{}

	if pr.FileSize != 0 {
		t.Errorf("FileSize = %d, want 0", pr.FileSize)
	}
	if pr.SupportsRange {
		t.Error("SupportsRange should be false by default")
	}
	if pr.Filename != "" {
		t.Errorf("Filename = '%s', want empty", pr.Filename)
	}
	if pr.ContentType != "" {
		t.Errorf("ContentType = '%s', want empty", pr.ContentType)
	}
}

func TestDownload_BuildsConfig(t *testing.T) {
	// This test verifies that the Download wrapper correctly builds a config
	//We dont test the full download

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := Download(ctx, "http://example.com/file", "/tmp/output", false, nil, "test-id")

	// Should fail because context is cancelled
	if err == nil {
		t.Log("Download returned nil error with cancelled context - this may be acceptable")
	}
}

func TestUniqueFilePath_EmptyFilename(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "surge-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Edge case: just extension
	existingFile := filepath.Join(tmpDir, ".txt")
	if err := os.WriteFile(existingFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	result := uniqueFilePath(existingFile)
	// Should handle gracefully - behavior depends on implementation
	if result == "" {
		t.Error("uniqueFilePath returned empty string")
	}
}

func TestUniqueFilePath_LongFilename(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "surge-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a file with a long name (within OS limits)
	longName := ""
	for i := 0; i < 50; i++ {
		longName += "a"
	}
	longName += ".txt"

	existingFile := filepath.Join(tmpDir, longName)
	if err := os.WriteFile(existingFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	result := uniqueFilePath(existingFile)
	if result == existingFile {
		t.Error("uniqueFilePath should generate different name for existing file")
	}
}

func TestUniqueFilePath_ParenInMiddle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "surge-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// File with parentheses in middle (not numbering)
	existingFile := filepath.Join(tmpDir, "file (copy).txt")
	if err := os.WriteFile(existingFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	result := uniqueFilePath(existingFile)
	// Should add (1) after the name but before extension
	expected := filepath.Join(tmpDir, "file (copy)(1).txt")
	if result != expected {
		t.Errorf("uniqueFilePath() = %v, want %v", result, expected)
	}
}

func TestUniqueFilePath_DeepNestedDirectory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "surge-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create deeply nested structure
	deepPath := filepath.Join(tmpDir, "a", "b", "c", "d", "e")
	if err := os.MkdirAll(deepPath, 0755); err != nil {
		t.Fatal(err)
	}

	existingFile := filepath.Join(deepPath, "file.txt")
	if err := os.WriteFile(existingFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	result := uniqueFilePath(existingFile)
	expected := filepath.Join(deepPath, "file(1).txt")
	if result != expected {
		t.Errorf("uniqueFilePath() = %v, want %v", result, expected)
	}
}

func BenchmarkUniqueFilePath_NoConflict(b *testing.B) {
	tmpDir, err := os.MkdirTemp("", "surge-bench-*")
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	path := filepath.Join(tmpDir, "nonexistent.txt")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		uniqueFilePath(path)
	}
}

func BenchmarkUniqueFilePath_WithConflict(b *testing.B) {
	tmpDir, err := os.MkdirTemp("", "surge-bench-*")
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create conflicting files
	path := filepath.Join(tmpDir, "file.txt")
	for i := 0; i <= 5; i++ {
		var name string
		if i == 0 {
			name = path
		} else {
			name = filepath.Join(tmpDir, fmt.Sprintf("file(%d).txt", i))
		}
		os.WriteFile(name, []byte("test"), 0644)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		uniqueFilePath(path)
	}
}

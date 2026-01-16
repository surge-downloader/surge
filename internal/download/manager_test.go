package download

import (
	"os"
	"path/filepath"
	"testing"
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

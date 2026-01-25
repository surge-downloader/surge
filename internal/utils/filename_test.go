package utils

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		// Basic cases
		{"simple filename", "file.zip", "file.zip"},
		{"filename with spaces", "  file.zip  ", "file.zip"},
		{"filename with backslash", "path\\file.zip", "file.zip"},
		{"filename with forward slash", "path/file.zip", "file.zip"},
		{"filename with colon", "file:name.zip", "file_name.zip"},
		{"filename with asterisk", "file*name.zip", "file_name.zip"},
		{"filename with question mark", "file?name.zip", "file_name.zip"},
		{"filename with quotes", "file\"name.zip", "file_name.zip"},
		{"filename with angle brackets", "file<name>.zip", "file_name_.zip"},
		{"filename with pipe", "file|name.zip", "file_name.zip"},
		{"dot only", ".", "."},
		// Note: slash test removed - filepath.Base("/") differs on Windows vs Unix
		// filepath.Base extracts "d.zip" from "a:b*c?d.zip" on Windows (treats a: as drive)
		{"multiple bad chars", "b*c?d.zip", "b_c_d.zip"},

		// Extended test cases
		{"unicode filename", "文件.zip", "文件.zip"},
		{"emoji in filename", "file🎉.zip", "file🎉.zip"},
		{"filename with extension only", ".gitignore", ".gitignore"},
		{"filename with multiple dots", "file.tar.gz", "file.tar.gz"},
		{"filename with hyphen", "my-file.zip", "my-file.zip"},
		{"filename with underscore", "my_file.zip", "my_file.zip"},
		{"mixed case", "MyFile.ZIP", "MyFile.ZIP"},
		{"all spaces becomes empty after trim", "   ", ""},
		{"tabs and newlines", "\tfile\n.zip", "file\n.zip"},
		{"very long extension", "file.verylongextension", "file.verylongextension"},
		{"numbers in name", "file123.zip", "file123.zip"},
		{"consecutive bad chars", "file***name.zip", "file___name.zip"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeFilename(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestDetermineFilename_PriorityOrder(t *testing.T) {
	// Helper to create a minimal ZIP header
	makeZipHeader := func(internalName string) []byte {
		h := make([]byte, 30+len(internalName))
		copy(h[0:4], []byte{0x50, 0x4B, 0x03, 0x04}) // Signature
		h[26] = byte(len(internalName))              // Filename length
		copy(h[30:], internalName)                   // Filename
		return h
	}

	zipContent := makeZipHeader("internal_id_123.txt")
	pdfContent := []byte("%PDF-1.4\n") // Magic bytes for PDF

	tests := []struct {
		name     string
		url      string
		headers  http.Header
		body     []byte
		expected string
	}{
		{
			name: "Priority 1: Content-Disposition beats all",
			url:  "https://example.com/file?filename=wrong.txt",
			headers: http.Header{
				"Content-Disposition": []string{`attachment; filename="correct.zip"`},
			},
			body:     zipContent,
			expected: "correct.zip",
		},
		{
			name:     "Priority 2: Query Param beats URL Path",
			url:      "https://example.com/download.php?filename=report.pdf",
			headers:  http.Header{},
			body:     pdfContent,
			expected: "report.pdf",
		},
		{
			name:     "Priority 3: URL Path beats ZIP Header (The fix you're testing)",
			url:      "https://example.com/logs_january.zip",
			headers:  http.Header{},
			body:     zipContent,
			expected: "logs_january.zip", // Should NOT be internal_id_123.txt
		},
		{
			name:     "Priority 4: ZIP Header used when URL is generic",
			url:      "", // Generic path
			headers:  http.Header{},
			body:     zipContent,
			expected: "internal_id_123.txt",
		},
		{
			name:     "Priority 5: MIME sniffing adds extension to generic name",
			url:      "https://example.com/get-file",
			headers:  http.Header{},
			body:     pdfContent,
			expected: "get-file.pdf",
		},
		{
			name:     "Fallback: Default name when everything is missing",
			url:      "",
			headers:  http.Header{},
			body:     []byte("random data"),
			expected: "download.bin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{
				Header: tt.headers,
				Body:   io.NopCloser(bytes.NewReader(tt.body)),
			}

			filename, _, err := DetermineFilename(tt.url, resp, false)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if filename != tt.expected {
				t.Errorf("got %q, want %q", filename, tt.expected)
			}
		})
	}
}

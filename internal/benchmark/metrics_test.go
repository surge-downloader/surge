package benchmark

import (
	"testing"
	"time"
)

// =============================================================================
// BenchmarkMetrics Tests
// =============================================================================

func TestNewBenchmarkMetrics(t *testing.T) {
	m := NewBenchmarkMetrics()

	if m == nil {
		t.Fatal("NewBenchmarkMetrics returned nil")
	}
	if m.StartTime.IsZero() {
		t.Error("StartTime should be set")
	}
	if m.BytesReceived.Load() != 0 {
		t.Error("BytesReceived should start at 0")
	}
	if m.RetryCount.Load() != 0 {
		t.Error("RetryCount should start at 0")
	}
}

func TestBenchmarkMetrics_RecordFirstByte(t *testing.T) {
	m := NewBenchmarkMetrics()

	if !m.FirstByteTime.IsZero() {
		t.Error("FirstByteTime should start zero")
	}

	m.RecordFirstByte()

	if m.FirstByteTime.IsZero() {
		t.Error("FirstByteTime should be set after RecordFirstByte")
	}

	firstTime := m.FirstByteTime

	// Second call should not update
	time.Sleep(10 * time.Millisecond)
	m.RecordFirstByte()

	if m.FirstByteTime != firstTime {
		t.Error("RecordFirstByte should only set once")
	}
}

func TestBenchmarkMetrics_RecordRetry(t *testing.T) {
	m := NewBenchmarkMetrics()

	for i := 0; i < 5; i++ {
		m.RecordRetry()
	}

	if m.RetryCount.Load() != 5 {
		t.Errorf("RetryCount = %d, want 5", m.RetryCount.Load())
	}
}

func TestBenchmarkMetrics_RecordBytes(t *testing.T) {
	m := NewBenchmarkMetrics()

	m.RecordBytes(1000)
	m.RecordBytes(500)
	m.RecordBytes(250)

	if m.BytesReceived.Load() != 1750 {
		t.Errorf("BytesReceived = %d, want 1750", m.BytesReceived.Load())
	}
}

func TestBenchmarkMetrics_RecordConnections(t *testing.T) {
	m := NewBenchmarkMetrics()

	m.RecordConnections(4)
	m.RecordConnections(8)
	m.RecordConnections(6)
	m.RecordConnections(8)

	if m.ConnectionMax.Load() != 8 {
		t.Errorf("ConnectionMax = %d, want 8", m.ConnectionMax.Load())
	}
	if m.SampleCount.Load() != 4 {
		t.Errorf("SampleCount = %d, want 4", m.SampleCount.Load())
	}
	// Average should be (4+8+6+8)/4 = 6.5
	expectedSum := int64(4 + 8 + 6 + 8)
	if m.ConnectionSum.Load() != expectedSum {
		t.Errorf("ConnectionSum = %d, want %d", m.ConnectionSum.Load(), expectedSum)
	}
}

func TestBenchmarkMetrics_Finish(t *testing.T) {
	m := NewBenchmarkMetrics()

	m.Finish(1000000)

	if m.EndTime.IsZero() {
		t.Error("EndTime should be set after Finish")
	}
	if m.TotalBytes != 1000000 {
		t.Errorf("TotalBytes = %d, want 1000000", m.TotalBytes)
	}
}

func TestBenchmarkMetrics_GetResults(t *testing.T) {
	m := NewBenchmarkMetrics()

	// Simulate activity
	time.Sleep(10 * time.Millisecond)
	m.RecordFirstByte()
	m.RecordBytes(1024 * 1024) // 1MB
	m.RecordConnections(4)
	m.RecordRetry()
	m.Finish(1024 * 1024)

	results := m.GetResults()

	if results.TotalTime <= 0 {
		t.Error("TotalTime should be positive")
	}
	if results.TTFB <= 0 {
		t.Error("TTFB should be positive")
	}
	if results.TotalBytes != 1024*1024 {
		t.Errorf("TotalBytes = %d, want %d", results.TotalBytes, 1024*1024)
	}
	if results.RetryCount != 1 {
		t.Errorf("RetryCount = %d, want 1", results.RetryCount)
	}
	if results.MaxConnections != 4 {
		t.Errorf("MaxConnections = %d, want 4", results.MaxConnections)
	}
	if results.AvgConnections != 4.0 {
		t.Errorf("AvgConnections = %f, want 4.0", results.AvgConnections)
	}
	if results.ThroughputMBps <= 0 {
		t.Error("ThroughputMBps should be positive")
	}
}

func TestBenchmarkMetrics_GetResults_NoConnections(t *testing.T) {
	m := NewBenchmarkMetrics()
	m.Finish(0)

	results := m.GetResults()

	if results.AvgConnections != 0 {
		t.Error("AvgConnections should be 0 with no samples")
	}
}

func TestBenchmarkMetrics_GetResults_ZeroTime(t *testing.T) {
	m := NewBenchmarkMetrics()
	m.EndTime = m.StartTime // Zero elapsed time
	m.TotalBytes = 1000

	results := m.GetResults()

	// Should handle zero time gracefully (avoid divide by zero)
	if results.TotalTime < 0 {
		t.Error("TotalTime should be non-negative")
	}
}

// =============================================================================
// BenchmarkResults Tests
// =============================================================================

func TestBenchmarkResults_String(t *testing.T) {
	results := BenchmarkResults{
		TotalTime:      10 * time.Second,
		TTFB:           100 * time.Millisecond,
		ThroughputMBps: 50.5,
		TotalBytes:     500 * 1024 * 1024,
		RetryCount:     2,
		MaxConnections: 8,
		AvgConnections: 6.5,
		MemoryUsedMB:   10.5,
	}

	output := results.String()

	if output == "" {
		t.Error("String() should return non-empty output")
	}

	// Should contain key information
	expectedSubstrings := []string{
		"Benchmark Results",
		"Throughput",
		"Total Time",
		"TTFB",
		"Retries",
		"Connections",
	}

	for _, substr := range expectedSubstrings {
		if !containsString(output, substr) {
			t.Errorf("Output should contain %q", substr)
		}
	}
}

// =============================================================================
// Helper Functions Tests
// =============================================================================

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
	}

	for _, tt := range tests {
		got := formatBytes(tt.bytes)
		if got != tt.expected {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.bytes, got, tt.expected)
		}
	}
}

func TestFormatInt(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "0"},
		{1, "1"},
		{123, "123"},
		{-456, "-456"},
	}

	for _, tt := range tests {
		got := formatInt(tt.input)
		if got != tt.expected {
			t.Errorf("formatInt(%d) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestFormatFloat(t *testing.T) {
	tests := []struct {
		f        float64
		decimals int
		expected string
	}{
		{0.0, 1, "0.0"},
		{1.5, 1, "1.5"},
		{3.14159, 2, "3.14"},
		{100.999, 1, "101.0"}, // Rounding
	}

	for _, tt := range tests {
		got := formatFloat(tt.f, tt.decimals)
		if got != tt.expected {
			t.Errorf("formatFloat(%f, %d) = %q, want %q", tt.f, tt.decimals, got, tt.expected)
		}
	}
}

func TestIntToString(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "0"},
		{1, "1"},
		{-1, "-1"},
		{12345, "12345"},
		{-99999, "-99999"},
	}

	for _, tt := range tests {
		got := intToString(tt.input)
		if got != tt.expected {
			t.Errorf("intToString(%d) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestInt64ToString(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0"},
		{1000, "1000"},
		{-5000, "-5000"},
	}

	for _, tt := range tests {
		got := int64ToString(tt.input)
		if got != tt.expected {
			t.Errorf("int64ToString(%d) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestFloatToString(t *testing.T) {
	tests := []struct {
		f        float64
		decimals int
		expected string
	}{
		{0.0, 1, "0.0"},
		{5.0, 2, "5.00"},
		{-3.5, 1, "-3.5"},
	}

	for _, tt := range tests {
		got := floatToString(tt.f, tt.decimals)
		if got != tt.expected {
			t.Errorf("floatToString(%f, %d) = %q, want %q", tt.f, tt.decimals, got, tt.expected)
		}
	}
}

func TestReplaceFirst(t *testing.T) {
	tests := []struct {
		s        string
		old      string
		new      string
		expected string
	}{
		{"hello world", "world", "there", "hello there"},
		{"foo bar foo", "foo", "baz", "baz bar foo"}, // Only first
		{"no match", "xyz", "abc", "no match"},
	}

	for _, tt := range tests {
		got := replaceFirst(tt.s, tt.old, tt.new)
		if got != tt.expected {
			t.Errorf("replaceFirst(%q, %q, %q) = %q, want %q",
				tt.s, tt.old, tt.new, got, tt.expected)
		}
	}
}

func TestSprintf(t *testing.T) {
	tests := []struct {
		format   string
		args     []interface{}
		expected string
	}{
		{"%d items", []interface{}{5}, "5 items"},
		{"%.1f MB/s", []interface{}{10.5}, "10.5 MB/s"},
		{"%c", []interface{}{byte('K')}, "K"},
	}

	for _, tt := range tests {
		got := sprintf(tt.format, tt.args...)
		if got != tt.expected {
			t.Errorf("sprintf(%q, %v) = %q, want %q",
				tt.format, tt.args, got, tt.expected)
		}
	}
}

// Helper function
func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// =============================================================================
// Benchmarks for Benchmark Code (Meta!)
// =============================================================================

func BenchmarkRecordBytes(b *testing.B) {
	m := NewBenchmarkMetrics()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		m.RecordBytes(1024)
	}
}

func BenchmarkRecordConnections(b *testing.B) {
	m := NewBenchmarkMetrics()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		m.RecordConnections(int32(i % 32))
	}
}

func BenchmarkFormatBytes(b *testing.B) {
	sizes := []int64{0, 1024, 1048576, 1073741824}
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		formatBytes(sizes[i%len(sizes)])
	}
}

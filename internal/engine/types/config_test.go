package types

import (
	"testing"
	"time"
)

func TestRuntimeConfig_Getters(t *testing.T) {
	t.Run("nil config returns defaults", func(t *testing.T) {
		var r *RuntimeConfig = nil

		if got := r.GetUserAgent(); got == "" {
			t.Error("GetUserAgent should return default, got empty")
		}
		if got := r.GetMaxConnectionsPerHost(); got != PerHostMax {
			t.Errorf("GetMaxConnectionsPerHost = %d, want %d", got, PerHostMax)
		}
		if got := r.GetMinChunkSize(); got != MinChunk {
			t.Errorf("GetMinChunkSize = %d, want %d", got, MinChunk)
		}
		if got := r.GetMaxChunkSize(); got != MaxChunk {
			t.Errorf("GetMaxChunkSize = %d, want %d", got, MaxChunk)
		}
		if got := r.GetTargetChunkSize(); got != TargetChunk {
			t.Errorf("GetTargetChunkSize = %d, want %d", got, TargetChunk)
		}
		if got := r.GetWorkerBufferSize(); got != WorkerBuffer {
			t.Errorf("GetWorkerBufferSize = %d, want %d", got, WorkerBuffer)
		}
		if got := r.GetMaxTaskRetries(); got != MaxTaskRetries {
			t.Errorf("GetMaxTaskRetries = %d, want %d", got, MaxTaskRetries)
		}
		if got := r.GetSlowWorkerThreshold(); got != SlowWorkerThreshold {
			t.Errorf("GetSlowWorkerThreshold = %f, want %f", got, SlowWorkerThreshold)
		}
		if got := r.GetSlowWorkerGracePeriod(); got != SlowWorkerGrace {
			t.Errorf("GetSlowWorkerGracePeriod = %v, want %v", got, SlowWorkerGrace)
		}
		if got := r.GetStallTimeout(); got != StallTimeout {
			t.Errorf("GetStallTimeout = %v, want %v", got, StallTimeout)
		}
		if got := r.GetSpeedEmaAlpha(); got != SpeedEMAAlpha {
			t.Errorf("GetSpeedEmaAlpha = %f, want %f", got, SpeedEMAAlpha)
		}
	})

	t.Run("zero values return defaults", func(t *testing.T) {
		r := &RuntimeConfig{} // All zero values

		if got := r.GetMaxConnectionsPerHost(); got != PerHostMax {
			t.Errorf("GetMaxConnectionsPerHost = %d, want %d", got, PerHostMax)
		}
		if got := r.GetMinChunkSize(); got != MinChunk {
			t.Errorf("GetMinChunkSize = %d, want %d", got, MinChunk)
		}
		if got := r.GetMaxChunkSize(); got != MaxChunk {
			t.Errorf("GetMaxChunkSize = %d, want %d", got, MaxChunk)
		}
		if got := r.GetWorkerBufferSize(); got != WorkerBuffer {
			t.Errorf("GetWorkerBufferSize = %d, want %d", got, WorkerBuffer)
		}
	})

	t.Run("custom values are returned", func(t *testing.T) {
		r := &RuntimeConfig{
			MaxConnectionsPerHost: 128,
			UserAgent:             "CustomAgent/1.0",
			MinChunkSize:          4 * MB,
			MaxChunkSize:          32 * MB,
			TargetChunkSize:       16 * MB,
			WorkerBufferSize:      1 * MB,
			MaxTaskRetries:        5,
			SlowWorkerThreshold:   0.75,
			SlowWorkerGracePeriod: 10 * time.Second,
			StallTimeout:          15 * time.Second,
			SpeedEmaAlpha:         0.5,
		}

		if got := r.GetMaxConnectionsPerHost(); got != 128 {
			t.Errorf("GetMaxConnectionsPerHost = %d, want 128", got)
		}
		if got := r.GetUserAgent(); got != "CustomAgent/1.0" {
			t.Errorf("GetUserAgent = %s, want CustomAgent/1.0", got)
		}
		if got := r.GetMinChunkSize(); got != 4*MB {
			t.Errorf("GetMinChunkSize = %d, want %d", got, 4*MB)
		}
		if got := r.GetMaxChunkSize(); got != 32*MB {
			t.Errorf("GetMaxChunkSize = %d, want %d", got, 32*MB)
		}
		if got := r.GetTargetChunkSize(); got != 16*MB {
			t.Errorf("GetTargetChunkSize = %d, want %d", got, 16*MB)
		}
		if got := r.GetWorkerBufferSize(); got != 1*MB {
			t.Errorf("GetWorkerBufferSize = %d, want %d", got, 1*MB)
		}
		if got := r.GetMaxTaskRetries(); got != 5 {
			t.Errorf("GetMaxTaskRetries = %d, want 5", got)
		}
		if got := r.GetSlowWorkerThreshold(); got != 0.75 {
			t.Errorf("GetSlowWorkerThreshold = %f, want 0.75", got)
		}
		if got := r.GetSlowWorkerGracePeriod(); got != 10*time.Second {
			t.Errorf("GetSlowWorkerGracePeriod = %v, want %v", got, 10*time.Second)
		}
		if got := r.GetStallTimeout(); got != 15*time.Second {
			t.Errorf("GetStallTimeout = %v, want %v", got, 15*time.Second)
		}
		if got := r.GetSpeedEmaAlpha(); got != 0.5 {
			t.Errorf("GetSpeedEmaAlpha = %f, want 0.5", got)
		}
	})
}

func TestSizeConstants(t *testing.T) {
	// Verify size constant relationships
	if KB != 1024 {
		t.Errorf("KB = %d, want 1024", KB)
	}
	if MB != 1024*KB {
		t.Errorf("MB = %d, want %d", MB, 1024*KB)
	}
	if GB != 1024*MB {
		t.Errorf("GB = %d, want %d", GB, 1024*MB)
	}

	// Verify chunk size constraints
	if MinChunk > MaxChunk {
		t.Errorf("MinChunk (%d) > MaxChunk (%d)", MinChunk, MaxChunk)
	}
	if TargetChunk < MinChunk || TargetChunk > MaxChunk {
		t.Errorf("TargetChunk (%d) not in range [%d, %d]", TargetChunk, MinChunk, MaxChunk)
	}

	// Verify alignment
	if AlignSize <= 0 {
		t.Errorf("AlignSize = %d, should be positive", AlignSize)
	}
	if AlignSize&(AlignSize-1) != 0 {
		t.Error("AlignSize should be a power of 2")
	}
}

func TestTimeoutConstants(t *testing.T) {
	// Verify timeouts are reasonable (not zero, not too long)
	timeouts := map[string]time.Duration{
		"DefaultIdleConnTimeout":       DefaultIdleConnTimeout,
		"DefaultTLSHandshakeTimeout":   DefaultTLSHandshakeTimeout,
		"DefaultResponseHeaderTimeout": DefaultResponseHeaderTimeout,
		"DefaultExpectContinueTimeout": DefaultExpectContinueTimeout,
		"DialTimeout":                  DialTimeout,
		"KeepAliveDuration":            KeepAliveDuration,
		"ProbeTimeout":                 ProbeTimeout,
		"HealthCheckInterval":          HealthCheckInterval,
		"SlowWorkerGrace":              SlowWorkerGrace,
		"StallTimeout":                 StallTimeout,
		"RetryBaseDelay":               RetryBaseDelay,
	}

	for name, timeout := range timeouts {
		if timeout <= 0 {
			t.Errorf("%s = %v, should be positive", name, timeout)
		}
		if timeout > 5*time.Minute {
			t.Errorf("%s = %v, seems too long", name, timeout)
		}
	}
}

func TestConnectionLimits(t *testing.T) {
	if PerHostMax <= 0 {
		t.Error("PerHostMax should be positive")
	}
	if PerHostMax > 256 {
		t.Error("PerHostMax seems too high")
	}
	// Check DefaultMaxIdleConns if available (int type)
	if DefaultMaxIdleConns <= 0 {
		t.Error("DefaultMaxIdleConns should be positive")
	}
}

func TestChannelBufferSizes(t *testing.T) {
	if ProgressChannelBuffer <= 0 {
		t.Error("ProgressChannelBuffer should be positive")
	}
}

func TestDownloadConfig_Fields(t *testing.T) {
	state := NewProgressState("test", 1000)
	runtime := &RuntimeConfig{MaxConnectionsPerHost: 8}

	cfg := DownloadConfig{
		URL:        "https://example.com/file.zip",
		OutputPath: "/tmp/file.zip",
		ID:         "download-123",
		Filename:   "file.zip",
		Verbose:    true,
		ProgressCh: nil,
		State:      state,
		Runtime:    runtime,
	}

	if cfg.URL != "https://example.com/file.zip" {
		t.Error("URL not set correctly")
	}
	if cfg.OutputPath != "/tmp/file.zip" {
		t.Error("OutputPath not set correctly")
	}
	if cfg.ID != "download-123" {
		t.Error("ID not set correctly")
	}
	if !cfg.Verbose {
		t.Error("Verbose not set correctly")
	}
	if cfg.State != state {
		t.Error("State not set correctly")
	}
	if cfg.Runtime != runtime {
		t.Error("Runtime not set correctly")
	}
}

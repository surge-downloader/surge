package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/surge-downloader/surge/internal/download/types"
)

// Settings holds all user-configurable application settings organized by category.
type Settings struct {
	General     GeneralSettings     `json:"general"`
	Connections ConnectionSettings  `json:"connections"`
	Chunks      ChunkSettings       `json:"chunks"`
	Performance PerformanceSettings `json:"performance"`
}

// GeneralSettings contains application behavior settings.
type GeneralSettings struct {
	DefaultDownloadDir     string `json:"default_download_dir"`
	WarnOnDuplicate        bool   `json:"warn_on_duplicate"`
	ExtensionPrompt        bool   `json:"extension_prompt"`
	AutoResume             bool   `json:"auto_resume"`
	SkipUpdateCheck        bool   `json:"skip_update_check"`
	MaxConcurrentDownloads int    `json:"max_concurrent_downloads"`
	ClipboardMonitor       bool   `json:"clipboard_monitor"`
}

// ConnectionSettings contains network connection parameters.
type ConnectionSettings struct {
	MaxConnectionsPerHost int    `json:"max_connections_per_host"`
	MaxGlobalConnections  int    `json:"max_global_connections"`
	UserAgent             string `json:"user_agent"`
}

// ChunkSettings contains download chunk configuration.
type ChunkSettings struct {
	MinChunkSize     int64 `json:"min_chunk_size"`
	MaxChunkSize     int64 `json:"max_chunk_size"`
	TargetChunkSize  int64 `json:"target_chunk_size"`
	WorkerBufferSize int   `json:"worker_buffer_size"`
}

// PerformanceSettings contains performance tuning parameters.
type PerformanceSettings struct {
	MaxTaskRetries        int           `json:"max_task_retries"`
	SlowWorkerThreshold   float64       `json:"slow_worker_threshold"`
	SlowWorkerGracePeriod time.Duration `json:"slow_worker_grace_period"`
	StallTimeout          time.Duration `json:"stall_timeout"`
	SpeedEmaAlpha         float64       `json:"speed_ema_alpha"`
}

// SettingMeta provides metadata for a single setting (for UI rendering).
type SettingMeta struct {
	Key         string // JSON key name
	Label       string // Human-readable label
	Description string // Help text displayed in right pane
	Type        string // "string", "int", "int64", "bool", "duration", "float64"
}

// GetSettingsMetadata returns metadata for all settings organized by category.
func GetSettingsMetadata() map[string][]SettingMeta {
	return map[string][]SettingMeta{
		"General": {
			{Key: "default_download_dir", Label: "Default Download Dir", Description: "Default directory for new downloads. Leave empty to use current directory.", Type: "string"},
			{Key: "warn_on_duplicate", Label: "Warn on Duplicate", Description: "Show warning when adding a download that already exists.", Type: "bool"},
			{Key: "extension_prompt", Label: "Extension Prompt", Description: "Prompt for confirmation when adding downloads via browser extension.", Type: "bool"},
			{Key: "auto_resume", Label: "Auto Resume", Description: "Automatically resume paused downloads on startup.", Type: "bool"},
			{Key: "skip_update_check", Label: "Skip Update Check", Description: "Disable automatic check for new versions on startup.", Type: "bool"},
			{Key: "max_concurrent_downloads", Label: "Max Concurrent Downloads", Description: "Maximum number of downloads running at once (1-10). Requires restart.", Type: "int"},
			{Key: "clipboard_monitor", Label: "Clipboard Monitor", Description: "Watch clipboard for URLs and prompt to download them.", Type: "bool"},
		},
		"Connections": {
			{Key: "max_connections_per_host", Label: "Max Connections/Host", Description: "Maximum concurrent connections per host (1-64).", Type: "int"},
			{Key: "max_global_connections", Label: "Max Global Connections", Description: "Maximum total concurrent connections across all downloads.", Type: "int"},
			{Key: "user_agent", Label: "User Agent", Description: "Custom User-Agent string for HTTP requests. Leave empty for default.", Type: "string"},
		},
		"Chunks": {
			{Key: "min_chunk_size", Label: "Min Chunk Size", Description: "Minimum download chunk size in MB (e.g., 2).", Type: "int64"},
			{Key: "max_chunk_size", Label: "Max Chunk Size", Description: "Maximum download chunk size in MB (e.g., 16).", Type: "int64"},
			{Key: "target_chunk_size", Label: "Target Chunk Size", Description: "Preferred chunk size in MB when splitting downloads.", Type: "int64"},
			{Key: "worker_buffer_size", Label: "Worker Buffer Size", Description: "I/O buffer size per worker in KB (e.g., 512).", Type: "int"},
		},
		"Performance": {
			{Key: "max_task_retries", Label: "Max Task Retries", Description: "Number of times to retry a failed chunk before giving up.", Type: "int"},
			{Key: "slow_worker_threshold", Label: "Slow Worker Threshold", Description: "Restart workers slower than this fraction of mean speed (0.0-1.0).", Type: "float64"},
			{Key: "slow_worker_grace_period", Label: "Slow Worker Grace", Description: "Grace period before checking worker speed (e.g., 5s).", Type: "duration"},
			{Key: "stall_timeout", Label: "Stall Timeout", Description: "Restart workers with no data for this duration (e.g., 5s).", Type: "duration"},
			{Key: "speed_ema_alpha", Label: "Speed EMA Alpha", Description: "Exponential moving average smoothing factor (0.0-1.0).", Type: "float64"},
		},
	}
}

// CategoryOrder returns the order of categories for UI tabs.
func CategoryOrder() []string {
	return []string{"General", "Connections", "Chunks", "Performance"}
}

const (
	KB = 1024
	MB = 1024 * KB
)

// DefaultSettings returns a new Settings instance with sensible defaults.
func DefaultSettings() *Settings {
	homeDir, _ := os.UserHomeDir()
	defaultDir := filepath.Join(homeDir, "Downloads")

	return &Settings{
		General: GeneralSettings{
			DefaultDownloadDir:     defaultDir,
			WarnOnDuplicate:        true,
			ExtensionPrompt:        false,
			AutoResume:             false,
			MaxConcurrentDownloads: 3,
			ClipboardMonitor:       true,
		},
		Connections: ConnectionSettings{
			MaxConnectionsPerHost: 32,
			MaxGlobalConnections:  100,
			UserAgent:             "", // Empty means use default UA
		},
		Chunks: ChunkSettings{
			MinChunkSize:     types.MinChunk,
			MaxChunkSize:     types.MaxChunk,
			TargetChunkSize:  types.TargetChunk,
			WorkerBufferSize: types.WorkerBuffer / 1024, // KB for display/config JSON, converted back later
		},
		Performance: PerformanceSettings{
			MaxTaskRetries:        3,
			SlowWorkerThreshold:   0.3,
			SlowWorkerGracePeriod: 5 * time.Second,
			StallTimeout:          3 * time.Second,
			SpeedEmaAlpha:         0.3,
		},
	}
}

// GetSettingsPath returns the path to the settings JSON file.
func GetSettingsPath() string {
	return filepath.Join(GetSurgeDir(), "settings.json")
}

// LoadSettings loads settings from disk. Returns defaults if file doesn't exist.
func LoadSettings() (*Settings, error) {
	path := GetSettingsPath()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// File doesn't exist, return defaults
			return DefaultSettings(), nil
		}
		return nil, err
	}

	settings := DefaultSettings() // Start with defaults to fill any missing fields
	if err := json.Unmarshal(data, settings); err != nil {
		return nil, err
	}

	return settings, nil
}

// SaveSettings saves settings to disk atomically.
func SaveSettings(s *Settings) error {
	path := GetSettingsPath()

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	// Atomic write: write to temp file, then rename
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return err
	}

	return os.Rename(tempPath, path)
}

// ToRuntimeConfig converts Settings to a downloader RuntimeConfig
// This is used to pass user settings to the download engine
type RuntimeConfig struct {
	MaxConnectionsPerHost int
	MaxGlobalConnections  int
	UserAgent             string
	MinChunkSize          int64
	MaxChunkSize          int64
	TargetChunkSize       int64
	WorkerBufferSize      int
	MaxTaskRetries        int
	SlowWorkerThreshold   float64
	SlowWorkerGracePeriod time.Duration
	StallTimeout          time.Duration
	SpeedEmaAlpha         float64
}

// ToRuntimeConfig creates a RuntimeConfig from user Settings
func (s *Settings) ToRuntimeConfig() *RuntimeConfig {
	return &RuntimeConfig{
		MaxConnectionsPerHost: s.Connections.MaxConnectionsPerHost,
		MaxGlobalConnections:  s.Connections.MaxGlobalConnections,
		UserAgent:             s.Connections.UserAgent,
		MinChunkSize:          s.Chunks.MinChunkSize,
		MaxChunkSize:          s.Chunks.MaxChunkSize,
		TargetChunkSize:       s.Chunks.TargetChunkSize,
		WorkerBufferSize:      s.Chunks.WorkerBufferSize * 1024, // Convert KB to Bytes
		MaxTaskRetries:        s.Performance.MaxTaskRetries,
		SlowWorkerThreshold:   s.Performance.SlowWorkerThreshold,
		SlowWorkerGracePeriod: s.Performance.SlowWorkerGracePeriod,
		StallTimeout:          s.Performance.StallTimeout,
		SpeedEmaAlpha:         s.Performance.SpeedEmaAlpha,
	}
}

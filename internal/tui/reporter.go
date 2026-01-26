package tui

import (
	"time"

	"github.com/surge-downloader/surge/internal/engine/types"
	"github.com/surge-downloader/surge/internal/engine/events"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	DefaultPollInterval = 150 * time.Millisecond
	SpeedSmoothingAlpha = 0.3 // EMA smoothing factor
)

type ProgressReporter struct {
	state        *types.ProgressState
	pollInterval time.Duration
	lastSpeed    float64
}

func NewProgressReporter(state *types.ProgressState) *ProgressReporter {
	return &ProgressReporter{
		state:        state,
		pollInterval: DefaultPollInterval,
		lastSpeed:    0,
	}
}

// PollCmd returns a tea.Cmd that polls the progress state after the interval
func (r *ProgressReporter) PollCmd() tea.Cmd {
	return tea.Tick(r.pollInterval, func(t time.Time) tea.Msg {
		// Check if download is done
		if r.state.Done.Load() {
			elapsed := time.Since(r.state.StartTime)
			total := r.state.TotalSize
			if total <= 0 {
				total = r.state.Downloaded.Load()
			}
			return events.DownloadCompleteMsg{
				DownloadID: r.state.ID,
				Elapsed:    elapsed,
				Total:      total,
			}
		}

		// Check for errors
		if err := r.state.GetError(); err != nil {
			return events.DownloadErrorMsg{
				DownloadID: r.state.ID,
				Err:        err,
			}
		}

		// Get current progress
		downloaded, total, elapsed, connections, sessionStart := r.state.GetProgress()

		// Calculate speed with EMA smoothing
		// Use session-specific bytes to avoid speed spike on resume
		sessionDownloaded := downloaded - sessionStart
		var instantSpeed float64
		if elapsed.Seconds() > 0 && sessionDownloaded > 0 {
			instantSpeed = float64(sessionDownloaded) / elapsed.Seconds()
		}

		if r.lastSpeed == 0 {
			r.lastSpeed = instantSpeed
		} else {
			r.lastSpeed = SpeedSmoothingAlpha*instantSpeed + (1-SpeedSmoothingAlpha)*r.lastSpeed
		}

		return events.ProgressMsg{
			DownloadID:        r.state.ID,
			Downloaded:        downloaded,
			Total:             total,
			Speed:             r.lastSpeed,
			ActiveConnections: int(connections),
		}
	})
}

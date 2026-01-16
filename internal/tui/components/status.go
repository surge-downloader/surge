package components

import (
	"github.com/junaid2005p/surge/internal/tui/colors"

	"github.com/charmbracelet/lipgloss"
)

// DownloadStatus represents the state of a download
type DownloadStatus int

const (
	StatusQueued DownloadStatus = iota
	StatusDownloading
	StatusPaused
	StatusComplete
	StatusError
)

// statusInfo holds the display properties for each status
type statusInfo struct {
	icon  string
	label string
	color lipgloss.Color
}

var statusMap = map[DownloadStatus]statusInfo{
	StatusQueued:      {"⋯", "Queued", colors.StatePaused},
	StatusDownloading: {"⬇", "Downloading", colors.StateDownloading},
	StatusPaused:      {"⏸", "Paused", colors.StatePaused},
	StatusComplete:    {"✔", "Completed", colors.StateDone},
	StatusError:       {"✖", "Error", colors.StateError},
}

// Icon returns the status icon
func (s DownloadStatus) Icon() string {
	if info, ok := statusMap[s]; ok {
		return info.icon
	}
	return "?"
}

// Label returns the status label
func (s DownloadStatus) Label() string {
	if info, ok := statusMap[s]; ok {
		return info.label
	}
	return "Unknown"
}

// Color returns the status color
func (s DownloadStatus) Color() lipgloss.Color {
	if info, ok := statusMap[s]; ok {
		return info.color
	}
	return colors.Gray
}

// Render returns the styled icon + label combination
func (s DownloadStatus) Render() string {
	info := statusMap[s]
	return lipgloss.NewStyle().Foreground(info.color).Render(info.icon + " " + info.label)
}

// RenderIcon returns just the styled icon
func (s DownloadStatus) RenderIcon() string {
	info := statusMap[s]
	return lipgloss.NewStyle().Foreground(info.color).Render(info.icon)
}

// DetermineStatus determines the DownloadStatus based on download state
// This centralizes the status determination logic that was duplicated in view.go and list.go
func DetermineStatus(done bool, paused bool, hasError bool, speed float64, downloaded int64) DownloadStatus {
	switch {
	case hasError:
		return StatusError
	case done:
		return StatusComplete
	case paused:
		return StatusPaused
	case speed == 0 && downloaded == 0:
		return StatusQueued
	default:
		return StatusDownloading
	}
}

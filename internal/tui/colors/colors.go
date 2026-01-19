package colors

import "github.com/charmbracelet/lipgloss"

// === Color Palette ===
// Vibrant "Cyberpunk" Neon Colors
var (
	NeonPurple = lipgloss.Color("#bd93f9")
	NeonPink   = lipgloss.Color("#ff79c6")
	NeonCyan   = lipgloss.Color("#8be9fd")
	DarkGray   = lipgloss.Color("#282a36") // Background
	Gray       = lipgloss.Color("#44475a") // Borders
	LightGray  = lipgloss.Color("#a9b1d6") // Brighter text for secondary info
	White      = lipgloss.Color("#f8f8f2")
)

// === Semantic State Colors ===
var (
	StateError       = lipgloss.Color("#ff5555") // 🔴 Red - Error/Stopped
	StatePaused      = lipgloss.Color("#ffb86c") // 🟡 Orange - Paused/Queued
	StateDownloading = lipgloss.Color("#50fa7b") // 🟢 Green - Downloading
	StateDone        = lipgloss.Color("#bd93f9") // 🔵 Purple - Completed
	Warning          = lipgloss.Color("#f1fa8c") // 🟡 Yellow - Rate Limited/Warning
)

// === Progress Bar Colors ===
const (
	ProgressStart = "#ff79c6" // Pink
	ProgressEnd   = "#bd93f9" // Purple
)

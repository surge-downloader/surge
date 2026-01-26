package tui

import (
	"time"

	"github.com/surge-downloader/surge/internal/engine/types"
)

const (
	// Timeouts and Intervals
	TickInterval = 200 * time.Millisecond
	// Input Dimensions
	InputWidth = 40

	// Layout Offsets and Padding
	HeaderWidthOffset      = 2
	ProgressBarWidthOffset = 4
	DefaultPaddingX        = 1
	DefaultPaddingY        = 0
	PopupPaddingY          = 1
	PopupPaddingX          = 2
	PopupWidth             = 70 // Consistent width for all popup dialogs

	// Viewport layout
	// Viewport layout
	CardHeight       = 2  // Compact rows for cyberpunk theme
	HeaderHeight     = 8  // Logo + Graph height
	FilePickerHeight = 12 // Height for file picker display

	// Channel Buffers - use consolidated constant from downloader
	ProgressChannelBuffer = types.ProgressChannelBuffer

	// Units - use consolidated constant from downloader
	Megabyte = types.Megabyte
)

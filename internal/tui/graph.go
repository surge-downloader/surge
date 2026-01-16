package tui

import (
	"fmt"
	"strings"

	"github.com/junaid2005p/surge/internal/utils"

	"github.com/charmbracelet/lipgloss"
)

// GraphStats contains the statistics to overlay on the graph
type GraphStats struct {
	DownloadSpeed float64 // Current download speed in MB/s
	DownloadTop   float64 // Top download speed in MB/s
	DownloadTotal int64   // Total downloaded bytes
}

var graphGradient = []lipgloss.Color{
	lipgloss.Color("#5f005f"), // Dark Purple (Bottom)
	lipgloss.Color("#8700af"), // Medium Purple
	lipgloss.Color("#af00d7"), // Bright Purple
	lipgloss.Color("#ff00ff"), // Neon Pink (Top)
}

// renderMultiLineGraph creates a multi-line bar graph with grid lines.
// The graph scales data to fill the full width.
// data: speed history data points
// width, height: dimensions of the graph
// maxVal: maximum value for scaling
// color: color for the data bars
// stats: stats to display in overlay box (pass nil to skip)
func renderMultiLineGraph(data []float64, width, height int, maxVal float64, color lipgloss.Color, stats *GraphStats) string {

	if width < 1 || height < 1 {
		return ""
	}

	// Styles
	gridStyle := lipgloss.NewStyle().Foreground(ColorGray)
	//barStyle := lipgloss.NewStyle().Foreground(color)

	// 1. Prepare the canvas with a Grid
	rows := make([][]string, height)
	for i := range rows {
		rows[i] = make([]string, width)
		for j := range rows[i] {
			if i == height-1 {
				// Bottom row: solid baseline
				rows[i][j] = gridStyle.Render("─")
			} else if i%2 == 0 {
				rows[i][j] = gridStyle.Render("╌")
			} else {
				rows[i][j] = " "
			}
		}
	}

	// Block characters for partial fills
	blocks := []string{" ", "▂", "▃", "▄", "▅", "▆", "▇", "█"}

	// Pre-calculate styles for every row to avoid re-creating them in the loop
	rowStyles := make([]lipgloss.Style, height)
	for y := 0; y < height; y++ {
		// Map height 'y' to an index in graphGradient
		// y=0 is the bottom in the loop logic below, but let's map it visually
		colorIdx := (y * len(graphGradient)) / height
		if colorIdx >= len(graphGradient) {
			colorIdx = len(graphGradient) - 1
		}
		rowStyles[y] = lipgloss.NewStyle().Foreground(graphGradient[colorIdx])
	}

	// 2. Scale data to fill full width
	// Each data point spans multiple columns to fill the graph
	if len(data) > 0 {
		colsPerPoint := float64(width) / float64(len(data))

		for i, val := range data {
			if val < 0 {
				val = 0
			}

			pct := val / maxVal
			if pct > 1.0 {
				pct = 1.0
			}
			totalSubBlocks := pct * float64(height) * 8.0

			// Calculate column range for this data point
			startCol := int(float64(i) * colsPerPoint)
			endCol := int(float64(i+1) * colsPerPoint)
			if endCol > width {
				endCol = width
			}

			// Draw the bar across all columns for this data point
			for col := startCol; col < endCol; col++ {
				for y := 0; y < height; y++ {
					rowIndex := height - 1 - y
					rowValue := totalSubBlocks - float64(y*8)

					var char string
					if rowValue <= 0 {
						continue
					} else if rowValue >= 8 {
						char = "█"
					} else {
						char = blocks[int(rowValue)]
					}

					// USE THE ROW STYLE HERE
					rows[rowIndex][col] = rowStyles[y].Render(char)
				}
			}
		}
	}

	// 3. Join rows to create the graph
	var graphBuilder strings.Builder
	for i, row := range rows {
		graphBuilder.WriteString(strings.Join(row, ""))
		if i < height-1 {
			graphBuilder.WriteRune('\n')
		}
	}
	graphStr := graphBuilder.String()

	// 4. If stats provided, overlay them on the right side
	if stats != nil {
		graphStr = overlayStatsBox(graphStr, stats, width, height)
	}

	return graphStr
}

// overlayStatsBox renders stats on top of the graph in the top-right area
func overlayStatsBox(graph string, stats *GraphStats, width, height int) string {
	// Create the stats box content - btop style
	valueStyle := lipgloss.NewStyle().Foreground(ColorNeonCyan).Bold(true)
	labelStyle := lipgloss.NewStyle().Foreground(ColorLightGray)
	headerStyle := lipgloss.NewStyle().Foreground(ColorNeonPink).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(ColorGray)

	speedMbps := stats.DownloadSpeed * 8
	topMbps := stats.DownloadTop * 8

	// Compact stats box like btop
	statsLines := []string{
		headerStyle.Render("download"),
		fmt.Sprintf("%s %s  %s",
			valueStyle.Render("▼"),
			valueStyle.Render(fmt.Sprintf("%.2f MB/s", stats.DownloadSpeed)),
			dimStyle.Render(fmt.Sprintf("(%.0f Mbps)", speedMbps)),
		),
		fmt.Sprintf("%s %s %s  %s",
			labelStyle.Render("▼"),
			labelStyle.Render("Top:"),
			valueStyle.Render(fmt.Sprintf("%.2f MB/s", stats.DownloadTop)),
			dimStyle.Render(fmt.Sprintf("(%.0f Mbps)", topMbps)),
		),
		fmt.Sprintf("%s %s %s",
			labelStyle.Render("▼"),
			labelStyle.Render("Total:"),
			valueStyle.Render(utils.ConvertBytesToHumanReadable(stats.DownloadTotal)),
		),
	}

	statsBox := lipgloss.JoinVertical(lipgloss.Right, statsLines...)
	statsWidth := lipgloss.Width(statsBox)
	statsHeight := lipgloss.Height(statsBox)

	if statsWidth >= width || statsHeight >= height {
		return graph
	}

	// Overlay by merging graph lines with stats lines on the right
	graphLines := strings.Split(graph, "\n")
	statsBoxLines := strings.Split(statsBox, "\n")

	for i := 0; i < len(statsBoxLines) && i < len(graphLines); i++ {
		graphLineWidth := lipgloss.Width(graphLines[i])
		statsLineWidth := lipgloss.Width(statsBoxLines[i])

		keepWidth := graphLineWidth - statsLineWidth - 1
		if keepWidth < 0 {
			keepWidth = 0
		}

		graphRunes := []rune(graphLines[i])
		if keepWidth < len(graphRunes) {
			graphLines[i] = string(graphRunes[:keepWidth]) + " " + statsBoxLines[i]
		} else {
			padding := keepWidth - len(graphRunes)
			graphLines[i] = graphLines[i] + strings.Repeat(" ", padding) + " " + statsBoxLines[i]
		}
	}

	return strings.Join(graphLines, "\n")
}

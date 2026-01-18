package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/surge-downloader/surge/internal/tui/components"
	"github.com/surge-downloader/surge/internal/utils"

	"github.com/charmbracelet/lipgloss"
)

// Define the Layout Ratios
const (
	ListWidthRatio = 0.6 // List takes 60% width
)

// renderModalWithOverlay renders a modal centered on screen with a dark overlay effect
func (m RootModel) renderModalWithOverlay(modal string) string {
	// Place modal centered with dark gray background fill for overlay effect
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal,
		lipgloss.WithWhitespaceChars("░"),
		lipgloss.WithWhitespaceForeground(lipgloss.Color("236")),
	)
}

func (m RootModel) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	// === Handle Modal States First ===
	// These overlays sit on top of the dashboard or replace it

	if m.state == InputState {
		labelStyle := lipgloss.NewStyle().Width(10).Foreground(ColorLightGray)
		// Centered popup - compact layout
		hintStyle := lipgloss.NewStyle().MarginLeft(1).Foreground(ColorLightGray) // Secondary
		if m.focusedInput == 1 {
			hintStyle = lipgloss.NewStyle().MarginLeft(1).Foreground(ColorNeonPink) // Highlighted
		}
		pathLine := lipgloss.JoinHorizontal(lipgloss.Left,
			labelStyle.Render("Path:"),
			m.inputs[1].View(),
			hintStyle.Render("[Tab] Browse"),
		)

		// Content layout - removing TitleStyle Render and adding spacers
		content := lipgloss.JoinVertical(lipgloss.Left,
			"", // Top spacer
			lipgloss.JoinHorizontal(lipgloss.Left, labelStyle.Render("URL:"), m.inputs[0].View()),
			"", // Spacer
			pathLine,
			"", // Spacer
			lipgloss.JoinHorizontal(lipgloss.Left, labelStyle.Render("Filename:"), m.inputs[2].View()),
			"", // Bottom spacer
			"",
			// Render dynamic help
			m.help.View(m.keys.Input),
		)

		// Apply padding to the content before boxing it
		paddedContent := lipgloss.NewStyle().Padding(0, 2).Render(content)

		box := renderBtopBox(PaneTitleStyle.Render(" Add Download "), "", paddedContent, 80, 11, ColorNeonPink)

		return m.renderModalWithOverlay(box)
	}

	if m.state == FilePickerState {
		picker := components.NewFilePickerModal(
			" Select Directory ",
			m.filepicker,
			m.help,
			m.keys.FilePicker,
			ColorNeonPink,
		)
		box := picker.RenderWithBtopBox(renderBtopBox, PaneTitleStyle)
		return m.renderModalWithOverlay(box)
	}

	if m.state == SettingsState {
		return m.viewSettings()
	}

	if m.state == DuplicateWarningState {
		modal := components.ConfirmationModal{
			Title:       "⚠ Duplicate Detected",
			Message:     "A download with this URL already exists",
			Detail:      truncateString(m.duplicateInfo, 50),
			Keys:        m.keys.Duplicate,
			Help:        m.help,
			BorderColor: ColorNeonPink,
			Width:       60,
			Height:      10,
		}
		box := modal.RenderWithBtopBox(renderBtopBox, PaneTitleStyle)
		return m.renderModalWithOverlay(box)
	}

	if m.state == ExtensionConfirmationState {
		modal := components.ConfirmationModal{
			Title:       "Extension Download",
			Message:     "Do you want to add this download?",
			Detail:      truncateString(m.pendingURL, 50),
			Keys:        m.keys.Extension,
			Help:        m.help,
			BorderColor: ColorNeonCyan,
			Width:       60,
			Height:      10,
		}
		box := modal.RenderWithBtopBox(renderBtopBox, PaneTitleStyle)
		return m.renderModalWithOverlay(box)
	}

	if m.state == BatchFilePickerState {
		picker := components.NewFilePickerModal(
			" Select URL File (.txt) ",
			m.filepicker,
			m.help,
			m.keys.FilePicker,
			ColorNeonCyan,
		)
		box := picker.RenderWithBtopBox(renderBtopBox, PaneTitleStyle)
		return m.renderModalWithOverlay(box)
	}

	if m.state == BatchConfirmState {
		urlCount := len(m.pendingBatchURLs)
		modal := components.ConfirmationModal{
			Title:       "Batch Import",
			Message:     fmt.Sprintf("Add %d downloads?", urlCount),
			Detail:      truncateString(m.batchFilePath, 50),
			Keys:        m.keys.BatchConfirm,
			Help:        m.help,
			BorderColor: ColorNeonCyan,
			Width:       60,
			Height:      10,
		}
		box := modal.RenderWithBtopBox(renderBtopBox, PaneTitleStyle)
		return m.renderModalWithOverlay(box)
	}

	if m.state == UpdateAvailableState && m.UpdateInfo != nil {
		modal := components.ConfirmationModal{
			Title:       "⬆ Update Available",
			Message:     fmt.Sprintf("A new version of Surge is available: %s", m.UpdateInfo.LatestVersion),
			Detail:      fmt.Sprintf("Current: %s", m.UpdateInfo.CurrentVersion),
			Keys:        m.keys.Update,
			Help:        m.help,
			BorderColor: ColorNeonCyan,
			Width:       60,
			Height:      12,
		}
		box := modal.RenderWithBtopBox(renderBtopBox, PaneTitleStyle)
		return m.renderModalWithOverlay(box)
	}

	// === MAIN DASHBOARD LAYOUT ===

	availableHeight := m.height - 2 // Margin
	availableWidth := m.width - 4   // Margin

	// Column Widths
	leftWidth := int(float64(availableWidth) * ListWidthRatio)
	rightWidth := availableWidth - leftWidth - 2 // -2 for spacing

	// --- LEFT COLUMN HEIGHTS ---
	serverBoxHeight := 3
	headerHeight := 11
	listHeight := availableHeight - headerHeight
	if listHeight < 10 {
		listHeight = 10
	}

	// --- RIGHT COLUMN HEIGHTS ---
	graphHeight := availableHeight / 3
	if graphHeight < 9 {
		graphHeight = 9
	}
	detailHeight := availableHeight - graphHeight
	if detailHeight < 10 {
		detailHeight = 10
	}

	// --- SECTION 1: HEADER & LOGO (Top Left) + LOG BOX (Top Right) ---
	logoText := `
   _______  ___________ ____ 
  / ___/ / / / ___/ __ '/ _ \
 (__  ) /_/ / /  / /_/ /  __/
/____/\__,_/_/   \__, /\___/ 
                /____/       `

	// Calculate stats for tab bar
	active, queued, downloaded := m.CalculateStats()

	// Logo takes ~45% of header width
	logoWidth := int(float64(leftWidth) * 0.45)
	logWidth := leftWidth - logoWidth - 2 // Rest for log box

	// Render logo centered in its box (move up to make room for server box)
	gradientLogo := ApplyGradient(logoText, ColorNeonPink, ColorNeonPurple)
	logoContent := lipgloss.NewStyle().Render(gradientLogo)
	logoBox := lipgloss.Place(logoWidth, headerHeight-serverBoxHeight, lipgloss.Center, lipgloss.Center, logoContent)

	// Server port box (below logo, same width)
	greenDot := lipgloss.NewStyle().Foreground(ColorStateDownloading).Render("●")
	serverText := lipgloss.NewStyle().Foreground(ColorNeonCyan).Bold(true).Render(fmt.Sprintf(" Listening on :%d", m.ServerPort))
	serverPortContent := lipgloss.NewStyle().
		Width(logoWidth - 4).
		Align(lipgloss.Center).
		Render(greenDot + serverText)
	serverBox := renderBtopBox("", PaneTitleStyle.Render(" Server "), serverPortContent, logoWidth, serverBoxHeight, ColorGray)

	// Combine logo and server box vertically
	logoColumn := lipgloss.JoinVertical(lipgloss.Left, logoBox, serverBox)

	// Render log viewport
	m.logViewport.Width = logWidth - 4      // Account for borders
	m.logViewport.Height = headerHeight - 4 // Account for borders and title
	logContent := m.logViewport.View()

	// Use different border color when focused
	logBorderColor := ColorGray
	if m.logFocused {
		logBorderColor = ColorNeonPink
	}
	logBox := renderBtopBox(PaneTitleStyle.Render(" Activity Log "), "", logContent, logWidth, headerHeight, logBorderColor)

	// Combine logo column and log box horizontally
	headerBox := lipgloss.JoinHorizontal(lipgloss.Top, logoColumn, logBox)

	// --- SECTION 2: SPEED GRAPH (Top Right) ---
	// Use GraphHistoryPoints from config (30 seconds of history)

	// Stats box width inside the Network Activity box
	statsBoxWidth := 18

	// Get the last 60 data points for the graph
	var graphData []float64
	if len(m.SpeedHistory) > GraphHistoryPoints {
		graphData = m.SpeedHistory[len(m.SpeedHistory)-GraphHistoryPoints:]
	} else {
		graphData = m.SpeedHistory
	}

	// Determine Max Speed for scaling
	maxSpeed := 0.0
	topSpeed := 0.0
	for _, v := range graphData {
		if v > maxSpeed {
			maxSpeed = v
		}
		if v > topSpeed {
			topSpeed = v
		}
	}

	if maxSpeed == 0 {
		maxSpeed = 1.0 // Default scale for empty graph
	} else {
		// Add headroom
		maxSpeed = maxSpeed * 1.1

		if maxSpeed < 1.0 {
			maxSpeed = 1.0
		}

		if maxSpeed >= 5 {
			maxSpeed = float64(int((maxSpeed+4.99)/5) * 5)
		} else {
			maxSpeed = float64(int(maxSpeed + 0.99))
		}
	}

	// Calculate Available Height for the Graph
	// graphHeight - Borders (2) - title area (1) - top/bottom padding (2)
	graphContentHeight := graphHeight - 5
	if graphContentHeight < 3 {
		graphContentHeight = 3
	}

	// Get current speed and calculate total downloaded
	currentSpeed := 0.0
	if len(m.SpeedHistory) > 0 {
		currentSpeed = m.SpeedHistory[len(m.SpeedHistory)-1]
	}

	// Calculate total downloaded across all downloads
	var totalDownloaded int64
	for _, d := range m.downloads {
		totalDownloaded += d.Downloaded
	}

	// Create stats content (left side inside box)
	speedMbps := currentSpeed * 8
	topMbps := topSpeed * 8

	valueStyle := lipgloss.NewStyle().Foreground(ColorNeonCyan).Bold(true)
	labelStyleStats := lipgloss.NewStyle().Foreground(ColorLightGray)
	dimStyle := lipgloss.NewStyle().Foreground(ColorGray)

	statsContent := lipgloss.JoinVertical(lipgloss.Left,
		fmt.Sprintf("%s %s", valueStyle.Render("▼"), valueStyle.Render(fmt.Sprintf("%.2f MB/s", currentSpeed))),
		dimStyle.Render(fmt.Sprintf("  (%.0f Mbps)", speedMbps)),
		"",
		fmt.Sprintf("%s %s", labelStyleStats.Render("Top:"), valueStyle.Render(fmt.Sprintf("%.2f", topSpeed))),
		dimStyle.Render(fmt.Sprintf("  (%.0f Mbps)", topMbps)),
		"",
		fmt.Sprintf("%s %s", labelStyleStats.Render("Total:"), valueStyle.Render(utils.ConvertBytesToHumanReadable(totalDownloaded))),
	)

	// Style stats with a border box
	statsBoxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorGray).
		Padding(0, 1).
		Width(statsBoxWidth).
		Height(graphContentHeight)
	statsBox := statsBoxStyle.Render(statsContent)

	// Graph takes remaining width after stats box
	axisWidth := 10                                              // Width for "X.X MB/s" labels
	graphAreaWidth := rightWidth - statsBoxWidth - axisWidth - 6 // borders + spacing
	if graphAreaWidth < 10 {
		graphAreaWidth = 10
	}

	// Render the Graph
	graphVisual := renderMultiLineGraph(graphData, graphAreaWidth, graphContentHeight, maxSpeed, ColorNeonPink, nil)

	// Create Y-axis (right side of graph)
	axisStyle := lipgloss.NewStyle().Width(axisWidth).Foreground(ColorNeonCyan).Align(lipgloss.Right)
	labelTop := axisStyle.Render(fmt.Sprintf("%.1f MB/s", maxSpeed))
	labelMid := axisStyle.Render(fmt.Sprintf("%.1f MB/s", maxSpeed/2))
	labelBot := axisStyle.Render("0 MB/s")

	var axisColumn string
	// Calculate exact spacing to match graph height
	// We use manual string concatenation because lipgloss.JoinVertical with explicit newlines
	// can sometimes add extra height that causes overflow.
	if graphContentHeight >= 5 {
		spacesTotal := graphContentHeight - 3
		spaceTop := spacesTotal / 2
		spaceBot := spacesTotal - spaceTop

		// Construction: TopLabel + (spaceTop newlines) + MidLabel + (spaceBot newlines) + BotLabel
		// Note: We use one newline to separate labels, plus spaceTop/Bot extra newlines.
		// Example: Top\n\nMid -> 1 empty line gap (spaceTop=1)

		axisColumn = labelTop + "\n" + strings.Repeat("\n", spaceTop) +
			labelMid + "\n" + strings.Repeat("\n", spaceBot) +
			labelBot

	} else if graphContentHeight >= 3 {
		spaces := graphContentHeight - 2
		axisColumn = labelTop + "\n" + strings.Repeat("\n", spaces) + labelBot
	} else {
		// Very small height - just show top and bottom
		axisColumn = labelTop + "\n" + labelBot
	}
	// Use a style to ensure alignment is preserved for the entire block if needed,
	// though individual lines are already aligned.
	axisColumn = lipgloss.NewStyle().Height(graphContentHeight).Align(lipgloss.Right).Render(axisColumn)

	// Combine: stats box (left) | graph (middle) | axis (right)
	graphWithAxis := lipgloss.JoinHorizontal(lipgloss.Top,
		statsBox,
		graphVisual,
		axisColumn,
	)

	// Add top and bottom padding inside the Network Activity box
	graphWithPadding := lipgloss.JoinVertical(lipgloss.Left,
		"", // Top padding
		graphWithAxis,
		"", // Bottom padding
	)

	// Render single network activity box containing stats + graph
	graphBox := renderBtopBox(PaneTitleStyle.Render(" Network Activity "), "", graphWithPadding, rightWidth, graphHeight, ColorNeonCyan)

	// --- SECTION 3: DOWNLOAD LIST (Bottom Left) ---
	// Tab Bar
	tabBar := renderTabs(m.activeTab, active, queued, downloaded)

	// Search bar (shown when search is active or has a query)
	var leftTitle string
	if m.searchActive || m.searchQuery != "" {
		searchIcon := lipgloss.NewStyle().Foreground(ColorNeonCyan).Render("> ")
		var searchDisplay string
		if m.searchActive {
			searchDisplay = m.searchInput.View() +
				lipgloss.NewStyle().Foreground(ColorGray).Render(" [esc exit]")
		} else {
			// Show query with clear hint
			searchDisplay = lipgloss.NewStyle().Foreground(ColorNeonPink).Render(m.searchQuery) +
				lipgloss.NewStyle().Foreground(ColorGray).Render(" [f to clear]")
		}
		// Pad the search bar to look like a title block
		leftTitle = " " + lipgloss.JoinHorizontal(lipgloss.Left, searchIcon, searchDisplay) + " "
	}

	// Render the bubbles list or centered empty message
	var listContent string
	if len(m.list.Items()) == 0 {
		// FIX: Reduced width (leftWidth-8) to account for padding (4) and borders (2) + safety
		// preventing the "floating bits" wrap-around artifact.
		listContentHeight := listHeight - 6
		if m.searchQuery != "" {
			listContent = lipgloss.Place(leftWidth-8, listContentHeight, lipgloss.Center, lipgloss.Center,
				lipgloss.NewStyle().Foreground(ColorNeonCyan).Render("No matching downloads"))
		} else {
			listContent = lipgloss.Place(leftWidth-8, listContentHeight, lipgloss.Center, lipgloss.Center,
				lipgloss.NewStyle().Foreground(ColorNeonCyan).Render("No downloads"))
		}
	} else {
		// ensure list fills the height
		m.list.SetHeight(listHeight - 4) // adjust for padding/tabs
		listContent = m.list.View()
	}

	// Build list inner content - No search bar inside
	listInnerContent := lipgloss.JoinVertical(lipgloss.Left, tabBar, listContent)
	listInner := lipgloss.NewStyle().Padding(1, 2).Render(listInnerContent)

	// Determine border color for downloads box based on focus
	downloadsBorderColor := ColorNeonPink
	if m.logFocused {
		downloadsBorderColor = ColorGray
	}
	listBox := renderBtopBox(leftTitle, PaneTitleStyle.Render(" Downloads "), listInner, leftWidth, listHeight, downloadsBorderColor)

	// --- SECTION 4: DETAILS PANE (Bottom Right) ---
	var detailContent string
	if d := m.GetSelectedDownload(); d != nil {
		detailContent = renderFocusedDetails(d, rightWidth-4)
	} else {
		detailContent = lipgloss.Place(rightWidth-4, detailHeight-4, lipgloss.Center, lipgloss.Center,
			lipgloss.NewStyle().Foreground(ColorNeonCyan).Render("No Download Selected"))
	}

	detailBox := renderBtopBox("", PaneTitleStyle.Render(" File Details "), detailContent, rightWidth, detailHeight, ColorGray)

	// --- ASSEMBLY ---

	// Left Column
	leftColumn := lipgloss.JoinVertical(lipgloss.Left, headerBox, listBox)

	// Right Column
	rightColumn := lipgloss.JoinVertical(lipgloss.Left, graphBox, detailBox)

	// Body
	body := lipgloss.JoinHorizontal(lipgloss.Top, leftColumn, rightColumn)

	// Footer - just keybindings
	footer := lipgloss.NewStyle().Padding(0, 1).Render(m.help.View(m.keys.Dashboard))

	return lipgloss.JoinVertical(lipgloss.Left,
		body,
		footer,
	)
}

// Helper to render the detailed info pane
func renderFocusedDetails(d *DownloadModel, w int) string {
	pct := 0.0
	if d.Total > 0 {
		pct = float64(d.Downloaded) / float64(d.Total)
	}

	// Consistent content width for centering
	contentWidth := w - 6

	// Status Box
	statusStr := getDownloadStatus(d)
	statusStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorGray).
		Width(contentWidth).
		Align(lipgloss.Center)

	statusBox := statusStyle.Render(statusStr)

	// Section divider
	divider := lipgloss.NewStyle().
		Foreground(ColorGray).
		Render(strings.Repeat("─", contentWidth))

	// File info section - Status removed from here
	fileInfoLines := []string{
		lipgloss.JoinHorizontal(lipgloss.Left, StatsLabelStyle.Render("Filename:"), StatsValueStyle.Render(truncateString(d.Filename, contentWidth-14))),
		lipgloss.JoinHorizontal(lipgloss.Left, StatsLabelStyle.Render("Filepath:"), StatsValueStyle.Render(truncateString(d.Destination, contentWidth-14))),
	}

	// Size display differs based on completed status
	if d.done {
		fileInfoLines = append(fileInfoLines,
			lipgloss.JoinHorizontal(lipgloss.Left, StatsLabelStyle.Render("Size:"), StatsValueStyle.Render(utils.ConvertBytesToHumanReadable(d.Total))),
		)
	} else {
		fileInfoLines = append(fileInfoLines,
			lipgloss.JoinHorizontal(lipgloss.Left, StatsLabelStyle.Render("Size:"), StatsValueStyle.Render(fmt.Sprintf("%s / %s", utils.ConvertBytesToHumanReadable(d.Downloaded), utils.ConvertBytesToHumanReadable(d.Total)))),
		)
	}

	fileInfo := lipgloss.JoinVertical(lipgloss.Left, fileInfoLines...)

	// URL section - always shown
	urlSection := lipgloss.JoinHorizontal(lipgloss.Left,
		StatsLabelStyle.Render("URL:"),
		lipgloss.NewStyle().Foreground(ColorLightGray).Render(truncateString(d.URL, contentWidth-14)),
	)

	IDSection := lipgloss.JoinHorizontal(lipgloss.Left,
		StatsLabelStyle.Render("ID:"),
		lipgloss.NewStyle().Foreground(ColorLightGray).Render(d.ID),
	)

	// For errored downloads, show error details
	if d.err != nil {
		errorStyle := lipgloss.NewStyle().Foreground(ColorStateError).Width(contentWidth - 4)
		errorLabel := lipgloss.NewStyle().Foreground(ColorStateError).Bold(true).Render("Error Details")

		// Word wrap the error message
		errMsg := d.err.Error()

		errorSection := lipgloss.JoinVertical(lipgloss.Left,
			errorLabel,
			"",
			errorStyle.Render(errMsg),
		)

		content := lipgloss.JoinVertical(lipgloss.Left,
			statusBox,
			"",
			fileInfo,
			"",
			divider,
			"",
			errorSection,
			"",
			divider,
			"",
			urlSection,
			IDSection,
		)

		return lipgloss.NewStyle().
			Padding(0, 2).
			Render(content)
	}

	// For completed downloads, show simplified view
	if d.done {
		// Calculate average speed for completed download
		var avgSpeedStr string
		if d.Elapsed.Seconds() > 0 {
			avgSpeed := float64(d.Total) / d.Elapsed.Seconds()
			avgSpeedStr = fmt.Sprintf("%.2f MB/s", avgSpeed/Megabyte)
		} else {
			avgSpeedStr = "N/A"
		}

		statsSection := lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.JoinHorizontal(lipgloss.Left, StatsLabelStyle.Render("Time Taken:"), StatsValueStyle.Render(d.Elapsed.Round(time.Second).String())),
			lipgloss.JoinHorizontal(lipgloss.Left, StatsLabelStyle.Render("Avg Speed:"), StatsValueStyle.Render(avgSpeedStr)),
		)

		content := lipgloss.JoinVertical(lipgloss.Left,
			statusBox,
			"",
			fileInfo,
			"",
			divider,
			"",
			statsSection,
			"",
			divider,
			"",
			urlSection,
			IDSection,
		)

		return lipgloss.NewStyle().
			Padding(0, 2).
			Render(content)
	}

	// For paused/queued downloads, show simplified view without ETA/Speed/Conns
	if d.paused || d.Speed == 0 {
		// Progress bar
		progressWidth := w - 12
		if progressWidth < 20 {
			progressWidth = 20
		}
		d.progress.Width = progressWidth
		progView := d.progress.ViewAs(pct)

		progressLabel := lipgloss.NewStyle().
			Foreground(ColorNeonCyan).
			Bold(true).
			Render("Progress")
		progressSection := lipgloss.JoinVertical(lipgloss.Left,
			progressLabel,
			"",
			lipgloss.NewStyle().MarginLeft(1).Render(progView),
		)

		// Elapsed time for paused downloads
		elapsedSection := lipgloss.JoinHorizontal(lipgloss.Left, StatsLabelStyle.Render("Elapsed:"), StatsValueStyle.Render(d.Elapsed.Round(time.Second).String()))

		content := lipgloss.JoinVertical(lipgloss.Left,
			statusBox,
			"",
			fileInfo,
			divider,
			"",
			progressSection,
			"",
			divider,
			"",
			elapsedSection,
			"",
			divider,
			"",
			urlSection,
			IDSection,
		)

		return lipgloss.NewStyle().
			Padding(0, 2).
			Render(content)
	}

	// For active downloads, show full view with progress, ETA, speed, connections
	// Progress bar with margins
	progressWidth := w - 12
	if progressWidth < 20 {
		progressWidth = 20
	}
	d.progress.Width = progressWidth
	progView := d.progress.ViewAs(pct)

	progressLabel := lipgloss.NewStyle().
		Foreground(ColorNeonCyan).
		Bold(true).
		Render("Progress")
	progressSection := lipgloss.JoinVertical(lipgloss.Left,
		progressLabel,
		"",
		lipgloss.NewStyle().MarginLeft(1).Render(progView),
	)

	// Calculate ETA
	var etaStr string
	if d.Speed > 0 && d.Total > 0 {
		remaining := d.Total - d.Downloaded
		etaSeconds := float64(remaining) / d.Speed
		etaDuration := time.Duration(etaSeconds) * time.Second
		etaStr = etaDuration.Round(time.Second).String()
	} else {
		etaStr = "∞"
	}

	// Stats section with ETA
	statsSection := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.JoinHorizontal(lipgloss.Left, StatsLabelStyle.Render("Speed:"), StatsValueStyle.Render(fmt.Sprintf("%.2f MB/s", d.Speed/Megabyte))),
		lipgloss.JoinHorizontal(lipgloss.Left, StatsLabelStyle.Render("ETA:"), StatsValueStyle.Render(etaStr)),
		lipgloss.JoinHorizontal(lipgloss.Left, StatsLabelStyle.Render("Conns:"), StatsValueStyle.Render(fmt.Sprintf("%d", d.Connections))),
		lipgloss.JoinHorizontal(lipgloss.Left, StatsLabelStyle.Render("Elapsed:"), StatsValueStyle.Render(d.Elapsed.Round(time.Second).String())),
	)

	// Combine all sections with status box at top
	content := lipgloss.JoinVertical(lipgloss.Left,
		statusBox,
		"",
		fileInfo,
		divider,
		"",
		progressSection,
		"",
		divider,
		"",
		statsSection,
		"",
		divider,
		"",
		urlSection,
		IDSection,
	)

	// Wrap in a container with reduced padding
	return lipgloss.NewStyle().
		Padding(0, 2).
		Render(content)
}

func getDownloadStatus(d *DownloadModel) string {
	status := components.DetermineStatus(d.done, d.paused, d.err != nil, d.Speed, d.Downloaded)
	return status.Render()
}

func (m RootModel) calcTotalSpeed() float64 {
	total := 0.0
	for _, d := range m.downloads {
		// Skip completed downloads
		if d.done {
			continue
		}
		total += d.Speed
	}
	return total / Megabyte
}

func (m RootModel) CalculateStats() (active, queued, downloaded int) {
	for _, d := range m.downloads {
		if d.done {
			downloaded++
		} else if d.Speed > 0 {
			active++
		} else {
			queued++
		}
	}
	return
}

func truncateString(s string, i int) string {
	runes := []rune(s)
	if len(runes) > i {
		return string(runes[:i]) + "..."
	}
	return s
}

func renderTabs(activeTab, activeCount, queuedCount, doneCount int) string {
	tabs := []components.Tab{
		{Label: "Queued", Count: queuedCount},
		{Label: "Active", Count: activeCount},
		{Label: "Done", Count: doneCount},
	}
	return components.RenderTabBar(tabs, activeTab, ActiveTabStyle, TabStyle)
}

// renderBtopBox creates a btop-style box with title embedded in the top border
// Supports left and right titles (e.g., search on left, pane name on right)
// Accepts pre-styled title strings
// Example: ╭─ 🔍 Search... ─────────── Downloads ─╮
// Delegates to components.RenderBtopBox for the actual rendering
func renderBtopBox(leftTitle, rightTitle string, content string, width, height int, borderColor lipgloss.Color) string {
	return components.RenderBtopBox(leftTitle, rightTitle, content, width, height, borderColor)
}

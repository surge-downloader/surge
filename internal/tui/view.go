package tui

import (
	"fmt"
	"time"

	"surge/internal/utils"

	"github.com/charmbracelet/lipgloss"
)

// View renders the entire TUI
func (m RootModel) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	if m.state == InputState {
		labelStyle := lipgloss.NewStyle().Width(10).Foreground(ColorSubtext)
		// Centered popup - compact layout
		// Always show browse hint to prevent box expansion, but dim when not focused
		hintStyle := lipgloss.NewStyle().MarginLeft(1).Foreground(ColorBorder) // Dimmed
		if m.focusedInput == 1 {
			hintStyle = lipgloss.NewStyle().MarginLeft(1).Foreground(ColorSecondary) // Highlighted
		}
		pathLine := lipgloss.JoinHorizontal(lipgloss.Left,
			labelStyle.Render("Path:"),
			m.inputs[1].View(),
			hintStyle.Render("[Tab] Browse"),
		)

		popup := lipgloss.JoinVertical(lipgloss.Left,
			TitleStyle.Render("Add New Download"),
			"",
			lipgloss.JoinHorizontal(lipgloss.Left, labelStyle.Render("URL:"), m.inputs[0].View()),
			pathLine,
			lipgloss.JoinHorizontal(lipgloss.Left, labelStyle.Render("Filename:"), m.inputs[2].View()),
			"",
			lipgloss.NewStyle().Foreground(ColorSubtext).Render("[Enter] Next/Start  [Esc] Cancel"),
		)

		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
			PanelStyle.Width(PopupWidth).Padding(1, 2).Render(popup),
		)
	}

	if m.state == DetailState {
		selected := m.downloads[m.cursor]
		details := renderDetails(selected)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
			PanelStyle.Padding(1, 2).Render(
				lipgloss.JoinVertical(lipgloss.Left,
					details,
					"",
					lipgloss.NewStyle().Foreground(ColorSubtext).Render("[Esc] Back"),
				),
			),
		)
	}

	if m.state == FilePickerState {
		pickerContent := lipgloss.JoinVertical(lipgloss.Left,
			TitleStyle.Render("Select Directory"),
			"",
			lipgloss.NewStyle().Foreground(ColorSubtext).Render(m.filepicker.CurrentDirectory),
			"",
			m.filepicker.View(),
			"",
			lipgloss.NewStyle().Foreground(ColorSubtext).Render("[.] Select Here  [H] Downloads  [Enter] Open  [Esc] Cancel"),
		)

		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
			PanelStyle.Width(PopupWidth).Padding(1, 2).Render(pickerContent),
		)
	}

	if m.state == HistoryState {
		// Full-screen history view similar to dashboard
		w := m.width - HeaderWidthOffset
		if w < 0 {
			w = 0
		}

		// Header
		header := lipgloss.JoinVertical(lipgloss.Left,
			HeaderStyle.Width(w).Render("📁  Download History"),
			StatsStyle.Render(fmt.Sprintf("Total: %d completed downloads", len(m.historyEntries))),
		)

		// Calculate visible entries
		availableHeight := m.height - HeaderHeight - 2
		cardHeight := 5 // Each history card is ~5 lines
		visibleCount := availableHeight / cardHeight
		if visibleCount < 1 {
			visibleCount = 1
		}

		// Scroll offset for history
		scrollOffset := 0
		if m.historyCursor >= visibleCount {
			scrollOffset = m.historyCursor - visibleCount + 1
		}
		endIdx := scrollOffset + visibleCount
		if endIdx > len(m.historyEntries) {
			endIdx = len(m.historyEntries)
		}

		var cards []string
		for i := scrollOffset; i < endIdx; i++ {
			e := m.historyEntries[i]
			isSelected := i == m.historyCursor

			style := CardStyle.Width(w - ProgressBarWidthOffset)
			if isSelected {
				style = SelectedCardStyle.Width(w - ProgressBarWidthOffset)
			}

			date := time.Unix(e.CompletedAt, 0).Format("Jan 02, 2006 at 15:04")
			size := utils.ConvertBytesToHumanReadable(e.TotalSize)

			content := lipgloss.JoinVertical(lipgloss.Left,
				CardTitleStyle.Render(truncateString(e.Filename, 80)),
				lipgloss.NewStyle().Foreground(ColorSubtext).Render(fmt.Sprintf("Size: %s  |  Completed: %s", size, date)),
				lipgloss.NewStyle().Foreground(ColorBorder).Italic(true).Render(truncateString(e.URL, 80)),
			)

			cards = append(cards, style.Render(content))
		}

		if len(m.historyEntries) == 0 {
			emptyMsg := lipgloss.Place(w, m.height-HeaderHeight-4, lipgloss.Center, lipgloss.Center,
				lipgloss.JoinVertical(lipgloss.Center,
					"No completed downloads yet.",
					"",
					"Downloads will appear here after completion.",
				),
			)
			return lipgloss.JoinVertical(lipgloss.Left,
				header,
				emptyMsg,
				lipgloss.NewStyle().Foreground(ColorSubtext).Padding(0, 1).Render("[Esc] Back to Dashboard"),
			)
		}

		listContent := lipgloss.JoinVertical(lipgloss.Left, cards...)

		// Scroll indicator
		scrollInfo := ""
		if len(m.historyEntries) > visibleCount {
			scrollInfo = fmt.Sprintf(" [%d-%d of %d]", scrollOffset+1, endIdx, len(m.historyEntries))
		}

		return lipgloss.JoinVertical(lipgloss.Left,
			header,
			listContent,
			"",
			lipgloss.NewStyle().Foreground(ColorSubtext).Padding(0, 1).Render("[↑/↓] Navigate  [d] Delete  [Esc] Back"+scrollInfo),
		)
	}

	if m.state == DuplicateWarningState {
		warningContent := lipgloss.JoinVertical(lipgloss.Center,
			lipgloss.NewStyle().Foreground(ColorWarning).Bold(true).Render("⚠ DUPLICATE DETECTED"),
			"",
			fmt.Sprintf("A download with this URL already exists:"),
			lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true).Render(truncateString(m.duplicateInfo, 50)),
			"",
			lipgloss.NewStyle().Foreground(ColorSubtext).Render("[C] Continue  [F] Focus Existing  [X] Cancel"),
		)

		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
			lipgloss.NewStyle().
				Border(lipgloss.DoubleBorder()).
				BorderForeground(ColorWarning).
				Padding(1, 3).
				Render(warningContent),
		)
	}

	// === Header ===
	active, queued, downloaded := m.CalculateStats()
	headerStats := fmt.Sprintf("Active: %d | Queued: %d | Downloaded: %d", active, queued, downloaded)

	totalSpeed := m.calcTotalSpeed()
	titleText := fmt.Sprintf("Surge  %s", m.PWD)
	speedText := fmt.Sprintf("Total Speed: %.2f MB/s", totalSpeed)

	w := m.width - HeaderWidthOffset
	// Ensure w is positive
	if w < 0 {
		w = 0
	}

	padding := 0
	if w > len(titleText)+len(speedText) {

		speedStart := (w - len(speedText)) / 2
		if speedStart > len(titleText) {
			padding = speedStart - len(titleText)
		} else {
			padding = 2 // Minimum spacing
		}
	} else {
		padding = 2
	}

	headerContent := fmt.Sprintf("%s%s%s", titleText, lipgloss.NewStyle().PaddingLeft(padding).Render(""), speedText)

	header := lipgloss.JoinVertical(lipgloss.Left,
		HeaderStyle.Width(w).Render(headerContent),
		StatsStyle.Render(headerStats),
	)

	if len(m.downloads) == 0 {
		return lipgloss.JoinVertical(lipgloss.Left,
			header,
			"",
			lipgloss.Place(m.width, m.height-6, lipgloss.Center, lipgloss.Center,
				lipgloss.JoinVertical(lipgloss.Center,
					"No active downloads.",
					"",
					"[g] Add Download  [q] Quit",
				),
			),
		)
	}

	// === List of Cards with Viewport Scrolling ===
	// Calculate how many cards can fit on screen
	availableHeight := m.height - HeaderHeight - 2 // Reserve space for footer
	visibleCount := availableHeight / CardHeight
	if visibleCount < 1 {
		visibleCount = 1
	}
	if visibleCount > len(m.downloads) {
		visibleCount = len(m.downloads)
	}

	// Determine visible range
	startIdx := m.scrollOffset
	endIdx := m.scrollOffset + visibleCount
	if endIdx > len(m.downloads) {
		endIdx = len(m.downloads)
	}

	var cards []string
	for i := startIdx; i < endIdx; i++ {
		cards = append(cards, renderCard(m.downloads[i], i == m.cursor, m.width-ProgressBarWidthOffset))
	}

	listContent := lipgloss.JoinVertical(lipgloss.Left, cards...)

	// Scroll indicator
	scrollInfo := ""
	if len(m.downloads) > visibleCount {
		scrollInfo = fmt.Sprintf(" [%d-%d of %d]", startIdx+1, endIdx, len(m.downloads))
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		listContent,
		"",
		lipgloss.NewStyle().Foreground(ColorSubtext).Padding(0, 1).Render("[g] Add  [h] History  [p] Pause/Resume  [d] Delete  [Enter] Details  [q] Quit"+scrollInfo),
	)
}

func renderCard(d *DownloadModel, selected bool, width int) string {
	style := CardStyle.Width(width)
	if selected {
		style = SelectedCardStyle.Width(width)
	}

	// Progress
	pct := 0.0
	if d.Total > 0 {
		pct = float64(d.Downloaded) / float64(d.Total)
	}
	d.progress.Width = width - ProgressBarWidthOffset
	// Use ViewAs to render at exact percentage (important for paused downloads)
	// ViewAs doesn't require animation commands unlike SetPercent + View
	progressBar := d.progress.ViewAs(pct)

	// Stats line
	eta := "N/A"
	if d.Speed > 0 && d.Total > 0 {
		remainingBytes := d.Total - d.Downloaded
		remainingSeconds := float64(remainingBytes) / d.Speed
		eta = time.Duration(remainingSeconds * float64(time.Second)).Round(time.Second).String()
	}

	stats := fmt.Sprintf("Speed: %.2f MB/s | Conns: %d | ETA: %s | %.0f%%", d.Speed/Megabyte, d.Connections, eta, pct*100)
	if d.done {
		stats = fmt.Sprintf("Completed | Size: %s", utils.ConvertBytesToHumanReadable(d.Total))
	} else if d.paused {
		stats = fmt.Sprintf("⏸ Paused | %s / %s", utils.ConvertBytesToHumanReadable(d.Downloaded), utils.ConvertBytesToHumanReadable(d.Total))
	} else if d.Speed == 0 && d.Downloaded == 0 {
		stats = "Status: Queued"
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		CardTitleStyle.Render(truncateString(d.Filename, 200)),
		progressBar,
		CardStatsStyle.Render(stats),
	)

	return style.Render(content)
}

func renderDetails(m *DownloadModel) string {
	title := TitleStyle.Render(m.Filename)

	if m.err != nil {
		return lipgloss.JoinVertical(lipgloss.Left,
			title,
			"",
			lipgloss.NewStyle().Foreground(ColorError).Render(fmt.Sprintf("Error: %v", m.err)),
		)
	}

	// Calculate stats
	percentage := 0.0
	if m.Total > 0 {
		percentage = float64(m.Downloaded) / float64(m.Total)
	}

	eta := "N/A"
	if m.Speed > 0 && m.Total > 0 {
		remainingBytes := m.Total - m.Downloaded
		remainingSeconds := float64(remainingBytes) / m.Speed
		eta = time.Duration(remainingSeconds * float64(time.Second)).Round(time.Second).String()
	}

	// Progress Bar
	m.progress.Width = 60
	progressBar := m.progress.ViewAs(percentage)

	stats := lipgloss.JoinVertical(lipgloss.Left,
		fmt.Sprintf("Progress:    %.2f%%", percentage*100),
		fmt.Sprintf("Size:        %s / %s", utils.ConvertBytesToHumanReadable(m.Downloaded), utils.ConvertBytesToHumanReadable(m.Total)),
		fmt.Sprintf("Speed:       %.2f MB/s", m.Speed/Megabyte),
		fmt.Sprintf("ETA:         %s", eta),
		fmt.Sprintf("Connections: %d", m.Connections),
		fmt.Sprintf("Elapsed:     %s", m.Elapsed.Round(time.Second)),
		fmt.Sprintf("URL:         %s", truncateString(m.URL, 50)),
	)

	return lipgloss.JoinVertical(lipgloss.Left,
		title,
		"",
		progressBar,
		"",
		stats,
	)
}

func (m RootModel) calcTotalSpeed() float64 {
	total := 0.0
	for _, d := range m.downloads {
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

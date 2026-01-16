package tui

import (
	"fmt"
	"io"

	"github.com/junaid2005p/surge/internal/tui/components"
	"github.com/junaid2005p/surge/internal/utils"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// DownloadItem implements list.Item interface for downloads
type DownloadItem struct {
	download *DownloadModel
}

func (i DownloadItem) Title() string {
	return i.download.Filename
}

func (i DownloadItem) Description() string {
	d := i.download

	// Get styled status using the shared component
	status := components.DetermineStatus(d.done, d.paused, d.err != nil, d.Speed, d.Downloaded)
	styledStatus := status.Render()

	// Build progress info
	pct := 0.0
	if d.Total > 0 {
		pct = float64(d.Downloaded) / float64(d.Total) * 100
	}

	// Format: "⬇ Downloading • 45% • 2.5 MB/s • 50 MB / 100 MB"
	sizeInfo := fmt.Sprintf("%s / %s",
		utils.ConvertBytesToHumanReadable(d.Downloaded),
		utils.ConvertBytesToHumanReadable(d.Total))

	speedInfo := ""
	if d.Speed > 0 {
		speedInfo = fmt.Sprintf(" • %.2f MB/s", d.Speed/Megabyte)
	}

	return fmt.Sprintf("%s • %.0f%%%s • %s", styledStatus, pct, speedInfo, sizeInfo)
}

func (i DownloadItem) FilterValue() string {
	return i.download.Filename
}

// Custom delegate for rendering download items
type downloadDelegate struct {
	keys *delegateKeyMap
}

type delegateKeyMap struct {
	pause  key.Binding
	delete key.Binding
}

func newDelegateKeyMap() *delegateKeyMap {
	return &delegateKeyMap{
		pause: key.NewBinding(
			key.WithKeys("p"),
			key.WithHelp("p", "pause/resume"),
		),
		delete: key.NewBinding(
			key.WithKeys("x"),
			key.WithHelp("x", "delete"),
		),
	}
}

func newDownloadDelegate() downloadDelegate {
	return downloadDelegate{
		keys: newDelegateKeyMap(),
	}
}

func (d downloadDelegate) Height() int  { return 2 }
func (d downloadDelegate) Spacing() int { return 1 }

func (d downloadDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd {
	return nil
}

func (d downloadDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	i, ok := listItem.(DownloadItem)
	if !ok {
		return
	}

	isSelected := index == m.Index()

	// Title styling
	titleStyle := lipgloss.NewStyle().
		Foreground(ColorWhite).
		Bold(true)

	// Description styling
	descStyle := lipgloss.NewStyle().
		Foreground(ColorLightGray)

	// Selected item styling
	if isSelected {
		titleStyle = titleStyle.Foreground(ColorNeonPink)
		descStyle = descStyle.Foreground(ColorNeonCyan)
	}

	// Left border indicator for selected item
	var prefix string
	if isSelected {
		prefix = lipgloss.NewStyle().
			Foreground(ColorNeonPink).
			Render("▌ ")
	} else {
		prefix = "  "
	}

	// Truncate title if needed
	width := m.Width() - 6
	if width < 20 {
		width = 20
	}
	title := i.Title()
	maxTitleWidth := width - 10
	if len(title) > maxTitleWidth {
		title = title[:maxTitleWidth-3] + "..."
	}

	// Render lines
	line1 := prefix + titleStyle.Render(title)
	line2 := prefix + descStyle.Render(i.Description())

	fmt.Fprintf(w, "%s\n%s", line1, line2)
}

// ShortHelp returns keybindings to show in the mini help view
func (d downloadDelegate) ShortHelp() []key.Binding {
	return []key.Binding{d.keys.pause, d.keys.delete}
}

// FullHelp returns keybindings for the expanded help view
func (d downloadDelegate) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{d.keys.pause, d.keys.delete},
	}
}

// NewDownloadList creates a new list.Model configured for downloads
func NewDownloadList(width, height int) list.Model {
	delegate := newDownloadDelegate()

	l := list.New([]list.Item{}, delegate, width, height)
	l.SetShowTitle(false) // Tab bar already shows the category
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	l.SetShowPagination(true)

	// Style the list
	l.Styles.Title = lipgloss.NewStyle().
		Foreground(ColorNeonPink).
		Bold(true).
		Padding(0, 1)

	l.Styles.FilterPrompt = lipgloss.NewStyle().
		Foreground(ColorNeonCyan)

	l.Styles.FilterCursor = lipgloss.NewStyle().
		Foreground(ColorNeonPink)

	// No items message - bright color for cyberpunk theme
	l.Styles.NoItems = lipgloss.NewStyle().
		Foreground(ColorNeonCyan).
		Padding(2, 0)

	l.SetStatusBarItemName("download", "downloads")

	return l
}

// UpdateListItems updates the list with filtered downloads based on active tab
func (m *RootModel) UpdateListItems() {
	// If the user manually switched tabs, don't try to preserve/follow selection
	if m.ManualTabSwitch {
		m.ManualTabSwitch = false
		filtered := m.getFilteredDownloads()
		items := make([]list.Item, len(filtered))
		for i, d := range filtered {
			items[i] = DownloadItem{download: d}
		}
		m.list.SetItems(items)
		// Reset cursor to top when manually switching tabs (standard behavior)
		m.list.Select(0)
		return
	}

	// Capture currently selected ID if we don't have a forced one
	targetID := m.SelectedDownloadID
	if targetID == "" {
		if d := m.GetSelectedDownload(); d != nil {
			targetID = d.ID
		}
	}

	filtered := m.getFilteredDownloads()
	items := make([]list.Item, len(filtered))
	for i, d := range filtered {
		items[i] = DownloadItem{download: d}
	}
	m.list.SetItems(items)

	// Restore selection
	found := false
	if targetID != "" {
		for i, item := range items {
			if di, ok := item.(DownloadItem); ok {
				if di.download.ID == targetID {
					m.list.Select(i)
					found = true
					break
				}
			}
		}

		// If we wanted to select something but it's not here, it might be in another tab
		if !found {
			// Find the download globally
			for _, d := range m.downloads {
				if d.ID == targetID {
					newTab := -1
					if d.done {
						newTab = TabDone
					} else if d.Speed > 0 {
						newTab = TabActive
					} else {
						newTab = TabQueued
					}

					// If it belongs to a different tab, switch to it
					if newTab != -1 && newTab != m.activeTab {
						m.activeTab = newTab
						m.updateListTitle()

						// Force selection for the recursive call
						m.SelectedDownloadID = targetID

						// Recurse to update list for the new tab
						m.UpdateListItems()
						return
					}
					break
				}
			}
		}
	}

	// Reset forced selection
	m.SelectedDownloadID = ""
}

// GetSelectedDownload returns the currently selected download from the list
func (m *RootModel) GetSelectedDownload() *DownloadModel {
	if item := m.list.SelectedItem(); item != nil {
		if di, ok := item.(DownloadItem); ok {
			return di.download
		}
	}
	return nil
}

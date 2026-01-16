package components

import (
	"github.com/junaid2005p/surge/internal/tui/colors"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
)

// ConfirmationModal renders a styled confirmation dialog box
type ConfirmationModal struct {
	Title       string
	Message     string
	Detail      string         // Optional additional detail line (e.g., filename, URL)
	Keys        help.KeyMap    // Key bindings to show in help
	Help        help.Model     // Help model for rendering keys
	BorderColor lipgloss.Color // Border color for the box
	Width       int
	Height      int
}

// ConfirmationKeyMap defines keybindings for a confirmation modal
type ConfirmationKeyMap struct {
	Confirm key.Binding
	Cancel  key.Binding
	Extra   key.Binding // Optional extra action (e.g., "Focus Existing")
}

// ShortHelp returns keybindings to show
func (k ConfirmationKeyMap) ShortHelp() []key.Binding {
	if k.Extra.Enabled() {
		return []key.Binding{k.Confirm, k.Extra, k.Cancel}
	}
	return []key.Binding{k.Confirm, k.Cancel}
}

// FullHelp returns keybindings for the expanded help view
func (k ConfirmationKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}

// NewConfirmationModal creates a modal with default styling
func NewConfirmationModal(title, message, detail string, keys help.KeyMap, helpModel help.Model, borderColor lipgloss.Color) ConfirmationModal {
	return ConfirmationModal{
		Title:       title,
		Message:     message,
		Detail:      detail,
		Keys:        keys,
		Help:        helpModel,
		BorderColor: borderColor,
		Width:       60,
		Height:      10,
	}
}

// View renders the confirmation modal content (without the box wrapper)
func (m ConfirmationModal) View() string {
	detailStyle := lipgloss.NewStyle().
		Foreground(colors.NeonPurple).
		Bold(true)

	// Build content - just message, detail, and help
	content := m.Message

	if m.Detail != "" {
		content = lipgloss.JoinVertical(lipgloss.Center,
			content,
			"",
			detailStyle.Render(m.Detail),
		)
	}

	content = lipgloss.JoinVertical(lipgloss.Center,
		content,
		"",
		m.Help.View(m.Keys),
	)

	return content
}

// RenderWithBtopBox renders the modal using the btop-style box with title in border
func (m ConfirmationModal) RenderWithBtopBox(
	renderBox func(leftTitle, rightTitle, content string, width, height int, borderColor lipgloss.Color) string,
	titleStyle lipgloss.Style,
) string {
	// Center content within the box
	innerWidth := m.Width - 4 // Account for borders
	innerHeight := m.Height - 2
	centeredContent := lipgloss.Place(innerWidth, innerHeight, lipgloss.Center, lipgloss.Center, m.View())
	// Title goes in the box border
	return renderBox(titleStyle.Render(" "+m.Title+" "), "", centeredContent, m.Width, m.Height, m.BorderColor)
}

// Centered returns the modal centered in the given dimensions (for standalone use)
func (m ConfirmationModal) Centered(width, height int) string {
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(m.BorderColor).
		Padding(1, 4)

	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center,
		boxStyle.Render(m.View()))
}

// RenderCentered renders the modal with btop-style box, centered on screen
// This combines RenderWithBtopBox + lipgloss.Place in a single call
func (m ConfirmationModal) RenderCentered(screenWidth, screenHeight int, titleStyle lipgloss.Style) string {
	// Center content within the box
	innerWidth := m.Width - 4 // Account for borders
	innerHeight := m.Height - 2
	centeredContent := lipgloss.Place(innerWidth, innerHeight, lipgloss.Center, lipgloss.Center, m.View())
	// Render the box with title in border
	box := RenderBtopBox(titleStyle.Render(" "+m.Title+" "), "", centeredContent, m.Width, m.Height, m.BorderColor)
	// Center on screen
	return lipgloss.Place(screenWidth, screenHeight, lipgloss.Center, lipgloss.Center, box)
}

package components

import (
	"github.com/junaid2005p/surge/internal/tui/colors"

	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/lipgloss"
)

// FilePickerModal represents a styled file picker modal
type FilePickerModal struct {
	Title       string
	Picker      filepicker.Model
	Help        help.Model
	HelpKeys    help.KeyMap
	BorderColor lipgloss.Color
	Width       int
	Height      int
}

// NewFilePickerModal creates a file picker modal with default styling
func NewFilePickerModal(title string, picker filepicker.Model, helpModel help.Model, helpKeys help.KeyMap, borderColor lipgloss.Color) FilePickerModal {
	return FilePickerModal{
		Title:       title,
		Picker:      picker,
		Help:        helpModel,
		HelpKeys:    helpKeys,
		BorderColor: borderColor,
		Width:       90,
		Height:      20,
	}
}

// View returns the inner content of the file picker (without the box)
func (m FilePickerModal) View() string {
	pathStyle := lipgloss.NewStyle().Foreground(colors.LightGray)

	content := lipgloss.JoinVertical(lipgloss.Left,
		"",
		pathStyle.Render(m.Picker.CurrentDirectory),
		"",
		m.Picker.View(),
		"",
		m.Help.View(m.HelpKeys),
	)

	return lipgloss.NewStyle().Padding(0, 2).Render(content)
}

// RenderWithBtopBox renders the modal using the btop-style box
func (m FilePickerModal) RenderWithBtopBox(
	renderBox func(leftTitle, rightTitle, content string, width, height int, borderColor lipgloss.Color) string,
	titleStyle lipgloss.Style,
) string {
	return renderBox(titleStyle.Render(m.Title), "", m.View(), m.Width, m.Height, m.BorderColor)
}

// Centered returns the modal centered in the given dimensions
func (m FilePickerModal) Centered(width, height int, box string) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// RenderCentered renders the modal with btop-style box, centered on screen
// This combines RenderWithBtopBox + Centered in a single call
func (m FilePickerModal) RenderCentered(screenWidth, screenHeight int, titleStyle lipgloss.Style) string {
	box := RenderBtopBox(titleStyle.Render(m.Title), "", m.View(), m.Width, m.Height, m.BorderColor)
	return lipgloss.Place(screenWidth, screenHeight, lipgloss.Center, lipgloss.Center, box)
}

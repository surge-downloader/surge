package components

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// Tab represents a single tab item
type Tab struct {
	Label string
	Count int // If >= 0, displays as "Label (Count)"; if < 0, displays just "Label"
}

// RenderTabBar renders a horizontal tab bar with the given tabs
// Each tab is wrapped in a rounded border box for consistent styling
// activeIndex specifies which tab is currently active (0-indexed)
func RenderTabBar(tabs []Tab, activeIndex int, activeStyle, inactiveStyle lipgloss.Style) string {
	var rendered []string
	for i, t := range tabs {
		var label string
		if t.Count >= 0 {
			label = fmt.Sprintf("%s (%d)", t.Label, t.Count)
		} else {
			label = t.Label
		}

		var tabStyle lipgloss.Style
		if i == activeIndex {
			tabStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(activeStyle.GetForeground()).
				Foreground(activeStyle.GetForeground()).
				Padding(0, 1).
				Bold(true)
		} else {
			tabStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(inactiveStyle.GetForeground()).
				Foreground(inactiveStyle.GetForeground()).
				Padding(0, 1)
		}

		rendered = append(rendered, tabStyle.Render(label))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, rendered...)
}

// RenderNumberedTabBar renders tabs with number prefixes like "[1] General"
// Each tab is wrapped in a rounded border box
func RenderNumberedTabBar(tabs []Tab, activeIndex int, activeStyle, inactiveStyle lipgloss.Style) string {
	var rendered []string
	for i, t := range tabs {
		var label string
		if t.Count >= 0 {
			label = fmt.Sprintf("[%d] %s (%d)", i+1, t.Label, t.Count)
		} else {
			label = fmt.Sprintf("[%d] %s", i+1, t.Label)
		}

		var tabStyle lipgloss.Style
		if i == activeIndex {
			tabStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(activeStyle.GetForeground()).
				Foreground(activeStyle.GetForeground()).
				Padding(0, 1).
				Bold(true)
		} else {
			tabStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(inactiveStyle.GetForeground()).
				Foreground(inactiveStyle.GetForeground()).
				Padding(0, 1)
		}

		rendered = append(rendered, tabStyle.Render(label))
	}
	return lipgloss.JoinHorizontal(lipgloss.Left, rendered...)
}

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
// activeIndex specifies which tab is currently active (0-indexed)
func RenderTabBar(tabs []Tab, activeIndex int, activeStyle, inactiveStyle lipgloss.Style) string {
	var rendered []string
	for i, t := range tabs {
		var style lipgloss.Style
		if i == activeIndex {
			style = activeStyle
		} else {
			style = inactiveStyle
		}

		var label string
		if t.Count >= 0 {
			label = fmt.Sprintf("%s (%d)", t.Label, t.Count)
		} else {
			label = t.Label
		}

		rendered = append(rendered, style.Render(label))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, rendered...)
}

// RenderNumberedTabBar renders tabs with number prefixes like "[1] General"
// Useful for settings-style tab bars
func RenderNumberedTabBar(tabs []Tab, activeIndex int, activeStyle, inactiveStyle lipgloss.Style) string {
	var rendered []string
	for i, t := range tabs {
		var style lipgloss.Style
		if i == activeIndex {
			style = activeStyle
		} else {
			style = inactiveStyle
		}

		label := fmt.Sprintf("[%d] %s", i+1, t.Label)
		rendered = append(rendered, style.Render(label))
	}
	return lipgloss.JoinHorizontal(lipgloss.Left, rendered...)
}

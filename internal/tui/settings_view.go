package tui

import (
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/junaid2005p/surge/internal/config"
	"github.com/junaid2005p/surge/internal/tui/components"

	"github.com/charmbracelet/lipgloss"
)

// viewSettings renders the Btop-style settings page
func (m RootModel) viewSettings() string {
	// Fixed smaller size for settings modal
	width := 90
	height := 18
	if m.width < width+4 {
		width = m.width - 4
	}
	if m.height < height+4 {
		height = m.height - 4
	}

	// Get category metadata
	categories := config.CategoryOrder()
	metadata := config.GetSettingsMetadata()

	// === TAB BAR ===
	var tabs []components.Tab
	for _, cat := range categories {
		tabs = append(tabs, components.Tab{Label: cat, Count: -1})
	}
	tabBar := components.RenderNumberedTabBar(tabs, m.SettingsActiveTab, ActiveTabStyle, TabStyle)

	// === CONTENT AREA ===
	currentCategory := categories[m.SettingsActiveTab]
	settingsMeta := metadata[currentCategory]

	// Get current settings values
	settingsValues := m.getSettingsValues(currentCategory)

	// Calculate column widths
	leftWidth := 26
	rightWidth := width - leftWidth - 6

	// === LEFT COLUMN: Settings List (names only) ===
	var listLines []string
	for i, meta := range settingsMeta {
		line := meta.Label

		// Highlight selected row
		if i == m.SettingsSelectedRow {
			style := lipgloss.NewStyle().Foreground(ColorNeonPink).Bold(true)
			cursor := "> "

			if meta.Key == "max_global_connections" {
				style = lipgloss.NewStyle().Foreground(ColorGray)
				cursor = "# "
			}

			line = style.Render(cursor + line)
		} else {
			style := lipgloss.NewStyle().Foreground(ColorLightGray)

			if meta.Key == "max_global_connections" {
				style = lipgloss.NewStyle().Foreground(lipgloss.Color("238")) // Darker gray
			}

			line = style.Render("  " + line)
		}

		listLines = append(listLines, line)
	}

	listContent := lipgloss.JoinVertical(lipgloss.Left, listLines...)

	// Wrap list in a bordered box
	listBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorGray).
		Width(leftWidth).
		PaddingLeft(1).
		PaddingRight(1).
		Render(listContent)

	// === RIGHT COLUMN: Value + Description ===
	var rightContent string
	if m.SettingsSelectedRow < len(settingsMeta) {
		meta := settingsMeta[m.SettingsSelectedRow]
		value := settingsValues[meta.Key]

		// Get unit suffix
		unit := m.getSettingUnit()
		unitStyle := lipgloss.NewStyle().Foreground(ColorGray)

		// Format value
		var valueStr string
		if m.SettingsIsEditing {
			// Show input with unit suffix (non-deletable)
			valueStr = m.SettingsInput.View() + unitStyle.Render(unit)
		} else {
			// Show formatted value with unit
			valueStr = formatSettingValueForEdit(value, meta.Type, meta.Key) + unitStyle.Render(unit)

			if meta.Key == "max_global_connections" {
				valueStr += " (Ignored)"
			}
		}

		// Show Tab hint for directory settings
		valueLabel := "Value: "
		if meta.Key == "default_download_dir" && !m.SettingsIsEditing {
			valueLabel = "[Tab] Browse: "
		}

		valueDisplay := lipgloss.NewStyle().
			Foreground(ColorNeonCyan).
			Bold(true).
			Render(valueLabel + valueStr)

		descDisplay := lipgloss.NewStyle().
			Foreground(ColorLightGray).
			Width(rightWidth - 2).
			Render(meta.Description)

		rightContent = valueDisplay + "\n\n" + descDisplay
	}

	rightBox := lipgloss.NewStyle().
		Width(rightWidth).
		PaddingLeft(1).
		Render(rightContent)

	// === COMBINE COLUMNS ===
	content := lipgloss.JoinHorizontal(lipgloss.Top, listBox, rightBox)

	// === HELP TEXT using Bubbles help ===
	helpText := m.help.View(m.keys.Settings)

	// === FINAL ASSEMBLY ===
	fullContent := lipgloss.JoinVertical(lipgloss.Left,
		"",
		tabBar,
		"",
		content,
		"",
		helpText,
	)

	box := renderBtopBox(PaneTitleStyle.Render(" Settings "), "", fullContent, width, height, ColorNeonPink)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// getSettingsValues returns a map of setting key -> value for a category
func (m RootModel) getSettingsValues(category string) map[string]interface{} {
	values := make(map[string]interface{})

	switch category {
	case "General":
		values["default_download_dir"] = m.Settings.General.DefaultDownloadDir
		values["warn_on_duplicate"] = m.Settings.General.WarnOnDuplicate
		values["extension_prompt"] = m.Settings.General.ExtensionPrompt
		values["auto_resume"] = m.Settings.General.AutoResume
	case "Connections":
		values["max_connections_per_host"] = m.Settings.Connections.MaxConnectionsPerHost
		values["max_global_connections"] = m.Settings.Connections.MaxGlobalConnections
		values["user_agent"] = m.Settings.Connections.UserAgent
	case "Chunks":
		values["min_chunk_size"] = m.Settings.Chunks.MinChunkSize
		values["max_chunk_size"] = m.Settings.Chunks.MaxChunkSize
		values["target_chunk_size"] = m.Settings.Chunks.TargetChunkSize
		values["worker_buffer_size"] = m.Settings.Chunks.WorkerBufferSize
	case "Performance":
		values["max_task_retries"] = m.Settings.Performance.MaxTaskRetries
		values["slow_worker_threshold"] = m.Settings.Performance.SlowWorkerThreshold
		values["slow_worker_grace_period"] = m.Settings.Performance.SlowWorkerGracePeriod
		values["stall_timeout"] = m.Settings.Performance.StallTimeout
		values["speed_ema_alpha"] = m.Settings.Performance.SpeedEmaAlpha
	}

	return values
}

// setSettingValue sets a setting value from string input
func (m *RootModel) setSettingValue(category, key, value string) error {
	metadata := config.GetSettingsMetadata()
	metas := metadata[category]

	var meta config.SettingMeta
	for _, sm := range metas {
		if sm.Key == key {
			meta = sm
			break
		}
	}

	switch category {
	case "General":
		return m.setGeneralSetting(key, value, meta.Type)
	case "Connections":
		return m.setConnectionsSetting(key, value, meta.Type)
	case "Chunks":
		return m.setChunksSetting(key, value, meta.Type)
	case "Performance":
		return m.setPerformanceSetting(key, value, meta.Type)
	}

	return nil
}

func (m *RootModel) setGeneralSetting(key, value, typ string) error {
	switch key {
	case "default_download_dir":
		m.Settings.General.DefaultDownloadDir = value
	case "warn_on_duplicate":
		m.Settings.General.WarnOnDuplicate = !m.Settings.General.WarnOnDuplicate
	case "extension_prompt":
		m.Settings.General.ExtensionPrompt = !m.Settings.General.ExtensionPrompt
	case "auto_resume":
		m.Settings.General.AutoResume = !m.Settings.General.AutoResume
	}
	return nil
}

func (m *RootModel) setConnectionsSetting(key, value, typ string) error {
	switch key {
	case "max_connections_per_host":
		if v, err := strconv.Atoi(value); err == nil {
			m.Settings.Connections.MaxConnectionsPerHost = v
		}
	case "max_global_connections":
		if v, err := strconv.Atoi(value); err == nil {
			m.Settings.Connections.MaxGlobalConnections = v
		}
	case "user_agent":
		m.Settings.Connections.UserAgent = value
	}
	return nil
}

func (m *RootModel) setChunksSetting(key, value, typ string) error {
	switch key {
	case "min_chunk_size":
		// Parse as MB and convert to bytes
		if v, err := strconv.ParseFloat(value, 64); err == nil {
			m.Settings.Chunks.MinChunkSize = int64(v * 1024 * 1024)
		}
	case "max_chunk_size":
		if v, err := strconv.ParseFloat(value, 64); err == nil {
			m.Settings.Chunks.MaxChunkSize = int64(v * 1024 * 1024)
		}
	case "target_chunk_size":
		if v, err := strconv.ParseFloat(value, 64); err == nil {
			m.Settings.Chunks.TargetChunkSize = int64(v * 1024 * 1024)
		}
	case "worker_buffer_size":
		// Keep buffer in KB
		if v, err := strconv.ParseFloat(value, 64); err == nil {
			m.Settings.Chunks.WorkerBufferSize = int(v * 1024)
		}
	}
	return nil
}

func (m *RootModel) setPerformanceSetting(key, value, typ string) error {
	switch key {
	case "max_task_retries":
		if v, err := strconv.Atoi(value); err == nil {
			m.Settings.Performance.MaxTaskRetries = v
		}
	case "slow_worker_threshold":
		if v, err := strconv.ParseFloat(value, 64); err == nil {
			// Clamp to valid range 0.0-1.0
			if v < 0.0 {
				v = 0.0
			} else if v > 1.0 {
				v = 1.0
			}
			m.Settings.Performance.SlowWorkerThreshold = v
		}
	case "slow_worker_grace_period":
		// Check if it's just a number, if so add "s"
		if _, err := strconv.ParseFloat(value, 64); err == nil {
			value += "s"
		}
		if v, err := time.ParseDuration(value); err == nil {
			m.Settings.Performance.SlowWorkerGracePeriod = v
		}
	case "stall_timeout":
		// Check if it's just a number, if so add "s"
		if _, err := strconv.ParseFloat(value, 64); err == nil {
			value += "s"
		}
		if v, err := time.ParseDuration(value); err == nil {
			m.Settings.Performance.StallTimeout = v
		}
	case "speed_ema_alpha":
		if v, err := strconv.ParseFloat(value, 64); err == nil {
			// Clamp to valid range 0.0-1.0
			if v < 0.0 {
				v = 0.0
			} else if v > 1.0 {
				v = 1.0
			}
			m.Settings.Performance.SpeedEmaAlpha = v
		}
	}
	return nil
}

// getCurrentSettingKey returns the key of the currently selected setting
func (m RootModel) getCurrentSettingKey() string {
	categories := config.CategoryOrder()
	metadata := config.GetSettingsMetadata()
	currentCategory := categories[m.SettingsActiveTab]
	settingsMeta := metadata[currentCategory]

	if m.SettingsSelectedRow < len(settingsMeta) {
		return settingsMeta[m.SettingsSelectedRow].Key
	}
	return ""
}

// getCurrentSettingType returns the type of the currently selected setting
func (m RootModel) getCurrentSettingType() string {
	categories := config.CategoryOrder()
	metadata := config.GetSettingsMetadata()
	currentCategory := categories[m.SettingsActiveTab]
	settingsMeta := metadata[currentCategory]

	if m.SettingsSelectedRow < len(settingsMeta) {
		return settingsMeta[m.SettingsSelectedRow].Type
	}
	return ""
}

// getSettingsCount returns the number of settings in the current category
func (m RootModel) getSettingsCount() int {
	categories := config.CategoryOrder()
	metadata := config.GetSettingsMetadata()
	currentCategory := categories[m.SettingsActiveTab]
	return len(metadata[currentCategory])
}

// getSettingUnit returns the unit suffix for the currently selected setting
func (m RootModel) getSettingUnit() string {
	key := m.getCurrentSettingKey()
	switch key {
	case "min_chunk_size", "max_chunk_size", "target_chunk_size":
		return " MB"
	case "worker_buffer_size":
		return " KB"
	case "max_task_retries":
		return " retries"
	case "slow_worker_grace_period", "stall_timeout":
		return " seconds"
	case "slow_worker_threshold", "speed_ema_alpha":
		return " (0.0-1.0)"
	default:
		return ""
	}
}

// formatSettingValueForEdit returns a plain value without units for editing
func formatSettingValueForEdit(value interface{}, typ, key string) string {
	switch key {
	case "min_chunk_size", "max_chunk_size", "target_chunk_size":
		if v, ok := value.(int64); ok {
			mb := float64(v) / (1024 * 1024)
			return fmt.Sprintf("%.1f", mb)
		}
	case "worker_buffer_size":
		v := reflect.ValueOf(value)
		if v.Kind() == reflect.Int {
			kb := float64(v.Int()) / 1024
			return fmt.Sprintf("%.0f", kb)
		}
	case "slow_worker_grace_period", "stall_timeout":
		// Show duration as plain seconds number (e.g., "5" instead of "5s")
		if d, ok := value.(time.Duration); ok {
			return fmt.Sprintf("%.0f", d.Seconds())
		}
	}
	// Default: use standard format
	return formatSettingValue(value, typ)
}

// formatSettingValue formats a setting value for display
func formatSettingValue(value interface{}, typ string) string {
	if value == nil {
		return "-"
	}

	switch typ {
	case "bool":
		if b, ok := value.(bool); ok {
			if b {
				return "True"
			}
			return "False"
		}
	case "duration":
		if d, ok := value.(time.Duration); ok {
			return d.String()
		}
	case "int64":
		if v, ok := value.(int64); ok {
			// Just display the raw number - units handled by getSettingUnit
			return fmt.Sprintf("%d", v)
		}
	case "int":
		v := reflect.ValueOf(value)
		if v.Kind() == reflect.Int {
			return fmt.Sprintf("%d", v.Int())
		}
	case "float64":
		if v, ok := value.(float64); ok {
			return fmt.Sprintf("%.2f", v)
		}
	case "string":
		if s, ok := value.(string); ok {
			if s == "" {
				return "(default)"
			}
			if len(s) > 30 {
				return s[:27] + "..."
			}
			return s
		}
	}

	// Fallback using reflection for numeric types
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Int, reflect.Int64:
		return fmt.Sprintf("%d", v.Int())
	case reflect.Float64:
		return fmt.Sprintf("%.2f", v.Float())
	default:
		return fmt.Sprintf("%v", value)
	}
}

// resetSettingToDefault resets a specific setting to its default value
func (m *RootModel) resetSettingToDefault(category, key string, defaults *config.Settings) {
	switch category {
	case "General":
		switch key {
		case "default_download_dir":
			m.Settings.General.DefaultDownloadDir = defaults.General.DefaultDownloadDir
		case "warn_on_duplicate":
			m.Settings.General.WarnOnDuplicate = defaults.General.WarnOnDuplicate
		case "extension_prompt":
			m.Settings.General.ExtensionPrompt = defaults.General.ExtensionPrompt
		case "auto_resume":
			m.Settings.General.AutoResume = defaults.General.AutoResume
		}
	case "Connections":
		switch key {
		case "max_connections_per_host":
			m.Settings.Connections.MaxConnectionsPerHost = defaults.Connections.MaxConnectionsPerHost
		case "max_global_connections":
			m.Settings.Connections.MaxGlobalConnections = defaults.Connections.MaxGlobalConnections
		case "user_agent":
			m.Settings.Connections.UserAgent = defaults.Connections.UserAgent
		}
	case "Chunks":
		switch key {
		case "min_chunk_size":
			m.Settings.Chunks.MinChunkSize = defaults.Chunks.MinChunkSize
		case "max_chunk_size":
			m.Settings.Chunks.MaxChunkSize = defaults.Chunks.MaxChunkSize
		case "target_chunk_size":
			m.Settings.Chunks.TargetChunkSize = defaults.Chunks.TargetChunkSize
		case "worker_buffer_size":
			m.Settings.Chunks.WorkerBufferSize = defaults.Chunks.WorkerBufferSize
		}
	case "Performance":
		switch key {
		case "max_task_retries":
			m.Settings.Performance.MaxTaskRetries = defaults.Performance.MaxTaskRetries
		case "slow_worker_threshold":
			m.Settings.Performance.SlowWorkerThreshold = defaults.Performance.SlowWorkerThreshold
		case "slow_worker_grace_period":
			m.Settings.Performance.SlowWorkerGracePeriod = defaults.Performance.SlowWorkerGracePeriod
		case "stall_timeout":
			m.Settings.Performance.StallTimeout = defaults.Performance.StallTimeout
		case "speed_ema_alpha":
			m.Settings.Performance.SpeedEmaAlpha = defaults.Performance.SpeedEmaAlpha
		}
	}
}

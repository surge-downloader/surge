package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"surge/internal/config"
	"surge/internal/downloader"
	"surge/internal/messages"
	"surge/internal/utils"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

// notificationTickMsg is sent to check if a notification should be cleared
type notificationTickMsg struct{}

// notificationTickCmd waits briefly then sends a tick to check notification expiry
func notificationTickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
		return notificationTickMsg{}
	})
}

// convertRuntimeConfig converts config.RuntimeConfig to downloader.RuntimeConfig
func convertRuntimeConfig(rc *config.RuntimeConfig) *downloader.RuntimeConfig {
	return &downloader.RuntimeConfig{
		MaxConnectionsPerHost: rc.MaxConnectionsPerHost,
		MaxGlobalConnections:  rc.MaxGlobalConnections,
		UserAgent:             rc.UserAgent,
		MinChunkSize:          rc.MinChunkSize,
		MaxChunkSize:          rc.MaxChunkSize,
		TargetChunkSize:       rc.TargetChunkSize,
		WorkerBufferSize:      rc.WorkerBufferSize,
		MaxTaskRetries:        rc.MaxTaskRetries,
		SlowWorkerThreshold:   rc.SlowWorkerThreshold,
		SlowWorkerGracePeriod: rc.SlowWorkerGracePeriod,
		StallTimeout:          rc.StallTimeout,
		SpeedEmaAlpha:         rc.SpeedEmaAlpha,
	}
}

// addLogEntry adds a log entry to the log viewport
func (m *RootModel) addLogEntry(msg string) {
	timestamp := time.Now().Format("15:04:05")
	entry := fmt.Sprintf("[%s] %s", timestamp, msg)
	m.logEntries = append(m.logEntries, entry)

	// Keep only the last 100 entries to prevent memory issues
	if len(m.logEntries) > 100 {
		m.logEntries = m.logEntries[len(m.logEntries)-100:]
	}

	// Update viewport content
	m.logViewport.SetContent(strings.Join(m.logEntries, "\n"))
	// Auto-scroll to bottom
	m.logViewport.GotoBottom()
}

// checkForDuplicate checks if a compatible download already exists
func (m RootModel) checkForDuplicate(url string) *DownloadModel {
	if !m.Settings.General.WarnOnDuplicate {
		return nil
	}
	normalizedInputURL := strings.TrimRight(url, "/")
	for _, d := range m.downloads {
		// Ignore completed downloads
		if d.done {
			continue
		}
		normalizedExistingURL := strings.TrimRight(d.URL, "/")
		if normalizedExistingURL == normalizedInputURL {
			return d
		}
	}
	return nil
}

// startDownload initiates a new download
func (m RootModel) startDownload(url, path, filename string) (RootModel, tea.Cmd) {
	// Generate unique filename to avoid overwriting
	// Note: We do this check here because it applies to ALL new downloads
	finalFilename := m.generateUniqueFilename(path, filename)

	nextID := uuid.New().String()
	newDownload := NewDownloadModel(nextID, url, "Queued", 0)
	m.downloads = append(m.downloads, newDownload)

	cfg := downloader.DownloadConfig{
		URL:        url,
		OutputPath: path,
		ID:         nextID,
		Filename:   finalFilename,
		Verbose:    false,
		ProgressCh: m.progressChan,
		State:      newDownload.state,
		Runtime:    convertRuntimeConfig(m.Settings.ToRuntimeConfig()),
	}

	utils.Debug("Adding to Queue: %s -> %s", url, finalFilename)
	m.Pool.Add(cfg)

	m.SelectedDownloadID = nextID
	m.activeTab = TabQueued
	m.UpdateListItems()

	return m, nil
}

// Update handles messages and updates the model
func (m RootModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case StartDownloadMsg:
		// Handle download request from HTTP server
		path := msg.Path
		if path == "" {
			path = m.Settings.General.DefaultDownloadDir
			if path == "" {
				path = "."
			}
		}

		// Check if extension prompt is enabled
		if m.Settings.General.ExtensionPrompt {
			m.pendingURL = msg.URL
			m.pendingPath = path
			m.pendingFilename = msg.Filename
			m.state = ExtensionConfirmationState
			return m, nil
		}

		// Check for duplicate URL
		if d := m.checkForDuplicate(msg.URL); d != nil {
			utils.Debug("Duplicate download detected from extension: %s", msg.URL)
			m.pendingURL = msg.URL
			m.pendingPath = path
			m.pendingFilename = msg.Filename
			m.duplicateInfo = d.Filename
			m.state = DuplicateWarningState
			return m, nil
		}

		return m.startDownload(msg.URL, path, msg.Filename)

	case messages.DownloadStartedMsg:
		// Find the download and update with real metadata + start polling
		for _, d := range m.downloads {
			if d.ID == msg.DownloadID {
				d.Filename = msg.Filename
				d.Total = msg.Total
				d.URL = msg.URL
				d.Destination = msg.DestPath
				// Reset start time to exclude probing
				d.StartTime = time.Now()
				// Update the progress state with real total size
				d.state.SetTotalSize(msg.Total)
				// Start polling for this download
				cmds = append(cmds, d.reporter.PollCmd())
				break
			}
		}
		// Update list items to reflect new filename
		m.UpdateListItems()
		// Add log entry
		m.addLogEntry(LogStyleStarted.Render("⬇ Started: " + msg.Filename))
		cmds = append(cmds, listenForActivity(m.progressChan))

	case messages.ProgressMsg:
		// Progress from polling reporter
		for _, d := range m.downloads {
			if d.ID == msg.DownloadID {
				// Don't update if already done or paused
				if d.done || d.paused {
					break
				}

				d.Downloaded = msg.Downloaded
				d.Speed = msg.Speed
				d.Elapsed = time.Since(d.StartTime)
				d.Connections = msg.ActiveConnections

				if d.Total > 0 {
					percentage := float64(d.Downloaded) / float64(d.Total)
					cmd := d.progress.SetPercent(percentage)
					cmds = append(cmds, cmd)
				}
				// Continue polling only if not done and not paused
				if !d.done && !d.paused {
					cmds = append(cmds, d.reporter.PollCmd())
				}

				// Add current speed to buffer for rolling average
				totalSpeed := m.calcTotalSpeed()
				m.speedBuffer = append(m.speedBuffer, totalSpeed)
				// Keep only last 10 samples (10 polls × 150ms = 1.5s window)
				if len(m.speedBuffer) > 10 {
					m.speedBuffer = m.speedBuffer[1:]
				}

				// Update global speed history every 500ms with rolling average
				if time.Since(m.lastSpeedHistoryUpdate) >= GraphUpdateInterval {
					// Calculate average of buffer
					var avgSpeed float64
					if len(m.speedBuffer) > 0 {
						for _, s := range m.speedBuffer {
							avgSpeed += s
						}
						avgSpeed /= float64(len(m.speedBuffer))
					}
					if len(m.SpeedHistory) > 0 {
						m.SpeedHistory = append(m.SpeedHistory[1:], avgSpeed)
					}
					m.lastSpeedHistoryUpdate = time.Now()
				}

				// Update list to show current progress
				m.UpdateListItems()
				break
			}
		}

	case messages.DownloadCompleteMsg:
		for _, d := range m.downloads {
			if d.ID == msg.DownloadID {
				if d.done {
					break
				}
				d.Downloaded = d.Total
				d.Elapsed = msg.Elapsed
				d.done = true
				// Set progress to 100%
				cmds = append(cmds, d.progress.SetPercent(1.0))

				// Add log entry
				speed := float64(d.Total) / msg.Elapsed.Seconds()
				m.addLogEntry(LogStyleComplete.Render(fmt.Sprintf("✔ Done: %s (%.2f MB/s)", d.Filename, speed/Megabyte)))

				// Persist to history (TUI has the correct filename from DownloadStartedMsg)
				_ = downloader.AddToMasterList(downloader.DownloadEntry{
					URLHash:     downloader.URLHash(d.URL),
					URL:         d.URL,
					DestPath:    "", // Not tracked in TUI model
					Filename:    d.Filename,
					Status:      "completed",
					TotalSize:   d.Total,
					CompletedAt: time.Now().Unix(),
				})

				break
			}
		}
		// Update list items
		m.UpdateListItems()
		cmds = append(cmds, listenForActivity(m.progressChan))

	case messages.DownloadErrorMsg:
		for _, d := range m.downloads {
			if d.ID == msg.DownloadID {
				d.err = msg.Err
				d.done = true
				// Add log entry
				m.addLogEntry(LogStyleError.Render("✖ Error: " + d.Filename))
				break
			}
		}
		m.UpdateListItems()
		cmds = append(cmds, listenForActivity(m.progressChan))

	case messages.DownloadPausedMsg:
		for _, d := range m.downloads {
			if d.ID == msg.DownloadID {
				d.paused = true
				d.Downloaded = msg.Downloaded
				d.Speed = 0 // Clear speed when paused
				// Add log entry
				m.addLogEntry(LogStylePaused.Render("⏸ Paused: " + d.Filename))
				break
			}
		}
		m.UpdateListItems()
		cmds = append(cmds, listenForActivity(m.progressChan))

	case messages.DownloadResumedMsg:
		for _, d := range m.downloads {
			if d.ID == msg.DownloadID {
				d.paused = false
				// Add log entry
				m.addLogEntry(LogStyleStarted.Render("▶ Resumed: " + d.Filename))
				// Restart polling
				cmds = append(cmds, d.reporter.PollCmd())
				break
			}
		}
		m.UpdateListItems()
		cmds = append(cmds, listenForActivity(m.progressChan))

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		// Calculate list dimensions
		// List goes in bottom-left pane
		availableWidth := msg.Width - 4
		leftWidth := int(float64(availableWidth) * ListWidthRatio)

		// Calculate list height (total height - header row - margins)
		topHeight := 9
		bottomHeight := msg.Height - topHeight - 5
		if bottomHeight < 10 {
			bottomHeight = 10
		}

		m.list.SetSize(leftWidth-2, bottomHeight-4)

		// Update list title based on active tab
		m.updateListTitle()
		m.UpdateListItems()
		return m, nil

	case notificationTickMsg:
		// Notification tick is still used but logs don't expire
		return m, nil

	// Handle filepicker messages for all message types when in FilePickerState
	default:
		if m.state == FilePickerState {
			var cmd tea.Cmd
			m.filepicker, cmd = m.filepicker.Update(msg)

			// Check if a directory was selected
			if didSelect, path := m.filepicker.DidSelectFile(msg); didSelect {
				// Check if we were browsing for settings
				if m.SettingsFileBrowsing {
					m.Settings.General.DefaultDownloadDir = path
					m.SettingsFileBrowsing = false
					m.state = SettingsState
					return m, nil
				}
				m.inputs[1].SetValue(path)
				m.state = InputState
				return m, nil
			}

			return m, cmd
		}

	case tea.KeyMsg:
		switch m.state {
		case DashboardState:
			// Tab switching with Q/W/E
			if msg.String() == "q" {
				m.activeTab = TabQueued
				m.ManualTabSwitch = true
				m.updateListTitle()
				m.UpdateListItems()
				return m, nil
			}
			if msg.String() == "w" {
				m.activeTab = TabActive
				m.ManualTabSwitch = true
				m.updateListTitle()
				m.UpdateListItems()
				return m, nil
			}
			if msg.String() == "e" {
				m.activeTab = TabDone
				m.ManualTabSwitch = true
				m.updateListTitle()
				m.UpdateListItems()
				return m, nil
			}
			// Ctrl+Q to quit
			if msg.String() == "ctrl+q" || msg.String() == "ctrl+c" {
				// Graceful shutdown: pause all active downloads to save state
				m.Pool.PauseAll()
				return m, tea.Quit
			}
			if msg.String() == "a" {
				m.state = InputState
				m.focusedInput = 0
				m.inputs[0].SetValue("")
				m.inputs[0].Focus()
				// Use default download dir from settings
				defaultDir := m.Settings.General.DefaultDownloadDir
				if defaultDir == "" {
					defaultDir = "."
				}
				m.inputs[1].SetValue(defaultDir)
				m.inputs[1].Blur()
				m.inputs[2].SetValue("")
				m.inputs[2].Blur()
				return m, nil
			}
			if msg.String() == "tab" {
				m.activeTab = (m.activeTab + 1) % 3
				m.ManualTabSwitch = true
				m.updateListTitle()
				m.UpdateListItems()
				return m, nil
			}
			// Delete download with D or X
			if msg.String() == "d" || msg.String() == "x" {
				// Don't process delete if list is filtering
				if m.list.FilterState() == list.Filtering {
					// Fall through to let list handle it
				} else if d := m.GetSelectedDownload(); d != nil {
					targetID := d.ID

					// Find index in real list
					realIdx := -1
					for i, dl := range m.downloads {
						if dl.ID == targetID {
							realIdx = i
							break
						}
					}

					if realIdx != -1 {
						dl := m.downloads[realIdx]

						// Cancel if active
						m.Pool.Cancel(dl.ID)

						// Delete state files
						if dl.URL != "" {
							_ = downloader.DeleteStateByURL(dl.URL)
						}

						// Delete partial/incomplete files (only for non-completed downloads)
						if !dl.done && dl.Destination != "" {
							// Delete the .surge partial file
							_ = os.Remove(dl.Destination + downloader.IncompleteSuffix)
						}

						// Remove from list
						m.downloads = append(m.downloads[:realIdx], m.downloads[realIdx+1:]...)
					}
					m.UpdateListItems()
					return m, nil
				}
			}
			if msg.String() == "h" {
				// Open history view
				if entries, err := downloader.LoadCompletedDownloads(); err == nil {
					m.historyEntries = entries
					m.historyCursor = 0
					m.state = HistoryState
				}
				return m, nil
			}

			// Pause/Resume toggle - get selected download from list
			if msg.String() == "p" {
				if d := m.GetSelectedDownload(); d != nil {
					if !d.done {
						if d.paused {
							// Resume: create config and add to pool
							d.paused = false
							d.state.Resume()
							cfg := downloader.DownloadConfig{
								URL:        d.URL,
								OutputPath: m.PWD, // Will be resolved in TUIDownload
								ID:         d.ID,
								Filename:   d.Filename,
								Verbose:    false,
								ProgressCh: m.progressChan,
								State:      d.state,
								Runtime:    convertRuntimeConfig(m.Settings.ToRuntimeConfig()),
							}
							m.Pool.Add(cfg)
							// Restart polling
							cmds = append(cmds, d.reporter.PollCmd())
						} else {
							m.Pool.Pause(d.ID)
						}
					}
				}
				m.UpdateListItems()
				return m, tea.Batch(cmds...)
			}

			// Toggle log focus with L
			if msg.String() == "l" {
				m.logFocused = !m.logFocused
				return m, nil
			}

			// Open settings with S
			if msg.String() == "s" {
				m.state = SettingsState
				m.SettingsActiveTab = 0
				m.SettingsSelectedRow = 0
				m.SettingsIsEditing = false
				return m, nil
			}

			// If log is focused, handle viewport scrolling
			if m.logFocused {
				switch msg.String() {
				case "esc":
					m.logFocused = false
					return m, nil
				case "j", "down":
					m.logViewport.LineDown(1)
					return m, nil
				case "k", "up":
					m.logViewport.LineUp(1)
					return m, nil
				case "g":
					m.logViewport.GotoTop()
					return m, nil
				case "G":
					m.logViewport.GotoBottom()
					return m, nil
				}
				return m, nil
			}

			// Pass messages to the list for navigation/filtering
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			cmds = append(cmds, cmd)
			return m, tea.Batch(cmds...)

		case DetailState:
			if msg.String() == "esc" || msg.String() == "q" || msg.String() == "enter" {
				m.state = DashboardState
				return m, nil
			}

		case InputState:
			if msg.String() == "esc" {
				m.state = DashboardState
				return m, nil
			}
			// Tab to open file picker when on path input
			if msg.String() == "tab" && m.focusedInput == 1 {
				m.state = FilePickerState
				// Reset filepicker to current directory
				m.filepicker.CurrentDirectory = m.PWD
				return m, m.filepicker.Init()
			}
			if msg.String() == "enter" {
				// Navigate through inputs: URL -> Path -> Filename -> Start
				if m.focusedInput < 2 {
					m.inputs[m.focusedInput].Blur()
					m.focusedInput++
					m.inputs[m.focusedInput].Focus()
					return m, nil
				}
				// Start download (on last input)
				url := m.inputs[0].Value()
				if url == "" {
					// URL is mandatory - don't start
					m.focusedInput = 0
					m.inputs[0].Focus()
					m.inputs[1].Blur()
					m.inputs[2].Blur()
					return m, nil
				}
				path := m.inputs[1].Value()
				if path == "" {
					path = "."
				}
				filename := m.inputs[2].Value()

				// Check for duplicate URL
				if d := m.checkForDuplicate(url); d != nil {
					m.pendingURL = url
					m.pendingPath = path
					m.pendingFilename = filename
					m.duplicateInfo = d.Filename
					m.state = DuplicateWarningState
					return m, nil
				}

				m.state = DashboardState
				return m.startDownload(url, path, filename)
			}

			// Up/Down navigation between inputs
			if msg.String() == "up" && m.focusedInput > 0 {
				m.inputs[m.focusedInput].Blur()
				m.focusedInput--
				m.inputs[m.focusedInput].Focus()
				return m, nil
			}
			if msg.String() == "down" && m.focusedInput < 2 {
				m.inputs[m.focusedInput].Blur()
				m.focusedInput++
				m.inputs[m.focusedInput].Focus()
				return m, nil
			}

			var cmd tea.Cmd
			m.inputs[m.focusedInput], cmd = m.inputs[m.focusedInput].Update(msg)
			return m, cmd

		case FilePickerState:
			if msg.String() == "esc" {
				// Cancel and return to appropriate state
				if m.SettingsFileBrowsing {
					m.SettingsFileBrowsing = false
					m.state = SettingsState
					return m, nil
				}
				m.state = InputState
				return m, nil
			}

			// H key to jump to Downloads folder
			if msg.String() == "h" || msg.String() == "H" {
				homeDir, _ := os.UserHomeDir()
				m.filepicker.CurrentDirectory = filepath.Join(homeDir, "Downloads")
				return m, m.filepicker.Init()
			}

			// '.' to select current directory
			if msg.String() == "." {
				if m.SettingsFileBrowsing {
					m.Settings.General.DefaultDownloadDir = m.filepicker.CurrentDirectory
					m.SettingsFileBrowsing = false
					m.state = SettingsState
					return m, nil
				}
				m.inputs[1].SetValue(m.filepicker.CurrentDirectory)
				m.state = InputState
				return m, nil
			}

			// Pass key to filepicker
			var cmd tea.Cmd
			m.filepicker, cmd = m.filepicker.Update(msg)

			// Check if a directory was selected
			if didSelect, path := m.filepicker.DidSelectFile(msg); didSelect {
				if m.SettingsFileBrowsing {
					m.Settings.General.DefaultDownloadDir = path
					m.SettingsFileBrowsing = false
					m.state = SettingsState
					return m, nil
				}
				// Set the path input value and return to input state
				m.inputs[1].SetValue(path)
				m.state = InputState
				return m, nil
			}

			return m, cmd

		case HistoryState:
			if msg.String() == "esc" || msg.String() == "q" {
				m.state = DashboardState
				return m, nil
			}
			if msg.String() == "up" || msg.String() == "k" {
				if m.historyCursor > 0 {
					m.historyCursor--
				}
				return m, nil
			}
			if msg.String() == "down" || msg.String() == "j" {
				if m.historyCursor < len(m.historyEntries)-1 {
					m.historyCursor++
				}
				return m, nil
			}
			if msg.String() == "d" || msg.String() == "x" {
				if m.historyCursor >= 0 && m.historyCursor < len(m.historyEntries) {
					entry := m.historyEntries[m.historyCursor]
					_ = downloader.RemoveFromMasterList(entry.URLHash)
					m.historyEntries, _ = downloader.LoadCompletedDownloads()
					if m.historyCursor >= len(m.historyEntries) && m.historyCursor > 0 {
						m.historyCursor--
					}
				}
				return m, nil
			}
			return m, nil

		case DuplicateWarningState:
			if msg.String() == "c" || msg.String() == "C" {
				// Continue anyway - startDownload handles unique filename generation
				m.state = DashboardState
				return m.startDownload(m.pendingURL, m.pendingPath, m.pendingFilename)
			}
			if msg.String() == "x" || msg.String() == "X" || msg.String() == "esc" {
				// Cancel - don't add
				m.state = DashboardState
				return m, nil
			}
			if msg.String() == "f" || msg.String() == "F" {
				// Focus existing download - find it and select in list
				for i, d := range m.getFilteredDownloads() {
					if d.URL == m.pendingURL {
						m.list.Select(i)
						break
					}
				}
				m.state = DashboardState
				return m, nil
			}
			return m, nil

		case ExtensionConfirmationState:
			if msg.String() == "y" || msg.String() == "Y" {
				// Confirmed - proceed to add (checking for duplicates first)
				if d := m.checkForDuplicate(m.pendingURL); d != nil {
					utils.Debug("Duplicate download detected after confirmation: %s", m.pendingURL)
					m.duplicateInfo = d.Filename
					m.state = DuplicateWarningState
					return m, nil
				}

				// No duplicate (or warning disabled) - add to queue
				m.state = DashboardState
				return m.startDownload(m.pendingURL, m.pendingPath, m.pendingFilename)
			}
			if msg.String() == "n" || msg.String() == "N" || msg.String() == "esc" {
				// Cancelled
				m.state = DashboardState
				return m, nil
			}
			return m, nil

		case SettingsState:
			// Handle editing mode first
			if m.SettingsIsEditing {
				switch msg.String() {
				case "esc":
					// Cancel editing
					m.SettingsIsEditing = false
					m.SettingsInput.Blur()
					return m, nil
				case "enter":
					// Commit the value
					categories := config.CategoryOrder()
					currentCategory := categories[m.SettingsActiveTab]
					key := m.getCurrentSettingKey()
					m.setSettingValue(currentCategory, key, m.SettingsInput.Value())
					m.SettingsIsEditing = false
					m.SettingsInput.Blur()
					return m, nil
				default:
					// Pass to text input
					var cmd tea.Cmd
					m.SettingsInput, cmd = m.SettingsInput.Update(msg)
					return m, cmd
				}
			}

			// Not editing - handle navigation
			switch msg.String() {
			case "esc":
				// Save settings and exit
				_ = config.SaveSettings(m.Settings)
				m.state = DashboardState
				return m, nil
			case "1":
				m.SettingsActiveTab = 0
				m.SettingsSelectedRow = 0
				return m, nil
			case "2":
				m.SettingsActiveTab = 1
				m.SettingsSelectedRow = 0
				return m, nil
			case "3":
				m.SettingsActiveTab = 2
				m.SettingsSelectedRow = 0
				return m, nil
			case "4":
				m.SettingsActiveTab = 3
				m.SettingsSelectedRow = 0
				return m, nil
			case "tab":
				// Open file browser for default_download_dir, otherwise switch category
				key := m.getCurrentSettingKey()
				if key == "default_download_dir" {
					m.SettingsFileBrowsing = true
					m.state = FilePickerState
					m.filepicker.CurrentDirectory = m.Settings.General.DefaultDownloadDir
					return m, m.filepicker.Init()
				}
				m.SettingsActiveTab = (m.SettingsActiveTab + 1) % 4
				m.SettingsSelectedRow = 0
				return m, nil
			case "shift+tab":
				m.SettingsActiveTab = (m.SettingsActiveTab + 3) % 4
				m.SettingsSelectedRow = 0
				return m, nil
			case "up", "k":
				if m.SettingsSelectedRow > 0 {
					m.SettingsSelectedRow--
				}
				return m, nil
			case "down", "j":
				maxRow := m.getSettingsCount() - 1
				if m.SettingsSelectedRow < maxRow {
					m.SettingsSelectedRow++
				}
				return m, nil
			case "enter":
				key := m.getCurrentSettingKey()
				// Prevent editing ignored settings
				if key == "max_global_connections" {
					return m, nil
				}

				// Toggle bool or enter edit mode for other types
				typ := m.getCurrentSettingType()
				if typ == "bool" {
					categories := config.CategoryOrder()
					currentCategory := categories[m.SettingsActiveTab]
					m.setSettingValue(currentCategory, key, "")
				} else {
					// Enter edit mode
					m.SettingsIsEditing = true
					// Pre-fill with current value (without units)
					categories := config.CategoryOrder()
					currentCategory := categories[m.SettingsActiveTab]
					values := m.getSettingsValues(currentCategory)
					m.SettingsInput.SetValue(formatSettingValueForEdit(values[key], typ, key))
					m.SettingsInput.Focus()
				}
				return m, nil
			case "r", "R":
				key := m.getCurrentSettingKey()
				if key == "max_global_connections" {
					return m, nil
				}

				// Reset current setting to default
				defaults := config.DefaultSettings()
				categories := config.CategoryOrder()
				currentCategory := categories[m.SettingsActiveTab]
				m.resetSettingToDefault(currentCategory, key, defaults)
				return m, nil

			}
			return m, nil
		}

	}

	// Propagate messages to progress bars - only update visible ones for performance
	for _, d := range m.downloads {
		var cmd tea.Cmd
		var newModel tea.Model
		newModel, cmd = d.progress.Update(msg)
		if p, ok := newModel.(progress.Model); ok {
			d.progress = p
		}
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	return m, tea.Batch(cmds...)
}

// updateListTitle updates the list title based on active tab
func (m *RootModel) updateListTitle() {
	switch m.activeTab {
	case TabQueued:
		m.list.Title = "📋 Queued"
	case TabActive:
		m.list.Title = "⬇️ Active"
	case TabDone:
		m.list.Title = "✅ Completed"
	}
}

// generateUniqueFilename creates a unique filename by appending (1), (2), etc.
// if the filename already exists in the destination folder OR in the current downloads list
func (m *RootModel) generateUniqueFilename(dir, filename string) string {
	if filename == "" {
		return filename // Let the downloader auto-detect
	}

	// Check if any download already has this filename
	existsInDownloads := func(name string) bool {
		for _, d := range m.downloads {
			// Don't check against completed downloads in the list,
			// as we rely on filesystem check for those.
			// But do check active/queued ones to avoid collision before file is created.
			if !d.done && d.Filename == name {
				return true
			}
		}
		return false
	}

	// Check if file exists on disk
	existsOnDisk := func(name string) bool {
		path := filepath.Join(dir, name)
		_, err := os.Stat(path)
		return !os.IsNotExist(err)
	}

	if !existsInDownloads(filename) && !existsOnDisk(filename) {
		return filename
	}

	// Split filename into base and extension
	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)

	// Try (1), (2), etc. until we find a unique one
	for i := 1; i <= 100; i++ {
		candidate := fmt.Sprintf("%s(%d)%s", base, i, ext)
		if !existsInDownloads(candidate) && !existsOnDisk(candidate) {
			return candidate
		}
	}

	// Fallback: just return original (shouldn't happen)
	return filename
}

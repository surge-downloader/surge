package tui

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/surge-downloader/surge/internal/clipboard"
	"github.com/surge-downloader/surge/internal/config"
	"github.com/surge-downloader/surge/internal/engine/events"
	"github.com/surge-downloader/surge/internal/engine/state"
	"github.com/surge-downloader/surge/internal/engine/types"
	"github.com/surge-downloader/surge/internal/utils"
	"github.com/surge-downloader/surge/internal/version"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

// notificationTickMsg is sent to check if a notification should be cleared
type notificationTickMsg struct{}

// UpdateCheckResultMsg is sent when the update check is complete
type UpdateCheckResultMsg struct {
	Info *version.UpdateInfo
}

// notificationTickCmd waits briefly then sends a tick to check notification expiry
func notificationTickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
		return notificationTickMsg{}
	})
}

// checkForUpdateCmd performs an async update check
func checkForUpdateCmd(currentVersion string) tea.Cmd {
	return func() tea.Msg {
		info, _ := version.CheckForUpdate(currentVersion)
		return UpdateCheckResultMsg{Info: info}
	}
}

// openBrowser opens a URL in the default browser
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default: // linux and others
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

// convertRuntimeConfig converts config.RuntimeConfig to types.RuntimeConfig
func convertRuntimeConfig(rc *config.RuntimeConfig) *types.RuntimeConfig {
	return &types.RuntimeConfig{
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

// readURLsFromFile reads URLs from a file, one per line (skips empty lines, comments, and duplicates)
func readURLsFromFile(filepath string) ([]string, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	var urls []string
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip empty lines and comments
		if line != "" && !strings.HasPrefix(line, "#") {
			// Normalize URL for duplicate detection
			normalized := strings.TrimRight(line, "/")
			if !seen[normalized] {
				seen[normalized] = true
				urls = append(urls, line)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	if len(urls) == 0 {
		return nil, fmt.Errorf("no URLs found in file")
	}

	return urls, nil
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

	cfg := types.DownloadConfig{
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

	case events.DownloadRequestMsg:
		// Handle download request from HTTP server (extension)
		// This message is only sent if user confirmation is required (ExtensionPrompt=true or Duplicate=true)

		path := msg.Path
		if path == "" {
			path = m.Settings.General.DefaultDownloadDir
			if path == "" {
				path = "."
			}
		}

		// Check for specific duplicate warning case
		// Even though root.go checks this, we re-check to determine WHICH screen to show
		duplicate := m.checkForDuplicate(msg.URL)

		if duplicate != nil && m.Settings.General.WarnOnDuplicate {
			utils.Debug("Duplicate download detected in TUI: %s", msg.URL)
			m.pendingURL = msg.URL
			m.pendingPath = path
			m.pendingFilename = msg.Filename
			m.duplicateInfo = duplicate.Filename
			m.state = DuplicateWarningState
			return m, nil
		}

		// Otherwise it's a general extension prompt
		if m.Settings.General.ExtensionPrompt {
			m.pendingURL = msg.URL
			m.pendingPath = path
			m.pendingFilename = msg.Filename
			m.state = ExtensionConfirmationState
			return m, nil
		}

		// Fallback: Just start it if for some reason we got here without needing a prompt
		// (Should not happen given root.go logic, but safe fallback)
		return m.startDownload(msg.URL, path, msg.Filename)

	case events.DownloadStartedMsg:

		// Check if we already have this download
		found := false
		for _, d := range m.downloads {
			if d.ID == msg.DownloadID {
				found = true
				break
			}
		}

		// If not found (external download), add it
		if !found {
			// Create new model matching the started download
			newDownload := NewDownloadModel(msg.DownloadID, msg.URL, msg.Filename, msg.Total)
			if msg.State != nil {
				newDownload.state = msg.State
				newDownload.reporter = NewProgressReporter(msg.State)
			}
			newDownload.Destination = msg.DestPath
			newDownload.state.SetTotalSize(msg.Total)

			m.downloads = append(m.downloads, newDownload)
			// Ensure polling starts for this new download
			// Note: PollCmd will be added in the loop below to handle both new and existing downloads uniformly
		}

		// Update metadata for all matching downloads (including the one just added)
		for _, d := range m.downloads {
			if d.ID == msg.DownloadID {
				d.Filename = msg.Filename
				d.Total = msg.Total
				d.Destination = msg.DestPath
				// Reset start time to exclude probing
				d.StartTime = time.Now()
				// Update the progress state with real total size
				d.state.SetTotalSize(msg.Total)
				// Start polling for this download (ensures UI updates speed/progress)
				cmds = append(cmds, d.reporter.PollCmd())
				break
			}
		}
		// Update list items to reflect new filename
		m.UpdateListItems()
		// Switch to active tab so user sees it
		if m.Settings.General.AutoResume {
			// Optional: switch tab logic if desired
		}
		// Add log entry
		m.addLogEntry(LogStyleStarted.Render("⬇ Started: " + msg.Filename))
		return m, tea.Batch(cmds...)

	case events.ProgressMsg:
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

	case events.DownloadCompleteMsg:
		for _, d := range m.downloads {
			if d.ID == msg.DownloadID {
				if d.done {
					break
				}
				d.Total = msg.Total
				d.Downloaded = d.Total
				d.Elapsed = msg.Elapsed
				d.done = true
				// Set progress to 100%
				cmds = append(cmds, d.progress.SetPercent(1.0))

				// Add log entry
				speed := float64(d.Total) / msg.Elapsed.Seconds()
				m.addLogEntry(LogStyleComplete.Render(fmt.Sprintf("✔ Done: %s (%.2f MB/s)", d.Filename, speed/Megabyte)))

				// Persist to history (TUI has the correct filename from DownloadStartedMsg)
				_ = state.AddToMasterList(types.DownloadEntry{
					URLHash:     state.URLHash(d.URL),
					ID:          d.ID,
					URL:         d.URL,
					DestPath:    d.Destination,
					Filename:    d.Filename,
					Status:      "completed",
					TotalSize:   d.Total,
					Downloaded:  d.Total,
					CompletedAt: time.Now().Unix(),
					TimeTaken:   d.Elapsed.Milliseconds(),
				})

				break
			}
		}
		// Update list items
		m.UpdateListItems()
		return m, tea.Batch(cmds...)

	case events.DownloadErrorMsg:
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
		return m, nil

	case events.DownloadPausedMsg:
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
		return m, nil

	case events.DownloadResumedMsg:
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
		return m, nil

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

	case UpdateCheckResultMsg:
		// Handle update check result
		if msg.Info != nil && msg.Info.UpdateAvailable {
			m.UpdateInfo = msg.Info
			m.state = UpdateAvailableState
		}
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

		if m.state == BatchFilePickerState {
			var cmd tea.Cmd
			m.filepicker, cmd = m.filepicker.Update(msg)

			// Check if a file was selected
			if didSelect, path := m.filepicker.DidSelectFile(msg); didSelect {
				// Read URLs from file
				urls, err := readURLsFromFile(path)
				if err != nil {
					m.addLogEntry(LogStyleError.Render("✖ Failed to read batch file: " + err.Error()))
					// Reset filepicker and return
					m.filepicker.FileAllowed = false
					m.filepicker.DirAllowed = true
					m.state = DashboardState
					return m, nil
				}

				// Store pending URLs and show confirmation
				m.pendingBatchURLs = urls
				m.batchFilePath = path

				// Reset filepicker to directory mode
				m.filepicker.FileAllowed = false
				m.filepicker.DirAllowed = true

				m.state = BatchConfirmState
				return m, nil
			}

			return m, cmd
		}

	case tea.KeyMsg:
		switch m.state {
		case DashboardState:
			// Handle search input FIRST when active (intercepts ALL keys)
			if m.searchActive {
				switch msg.String() {
				case "esc":
					// Cancel search and clear query
					m.searchActive = false
					m.searchInput.Blur()
					m.searchQuery = ""
					m.searchInput.SetValue("")
					m.UpdateListItems()
					return m, nil
				case "enter":
					// Commit search (keep filter applied)
					m.searchActive = false
					m.searchInput.Blur()
					return m, nil
				default:
					// All other keys go to search input
					var cmd tea.Cmd
					m.searchInput, cmd = m.searchInput.Update(msg)
					m.searchQuery = m.searchInput.Value()
					m.UpdateListItems()
					return m, cmd
				}
			}

			// Toggle search with F
			if key.Matches(msg, m.keys.Dashboard.Search) {
				if m.searchQuery != "" {
					// Clear existing search
					m.searchQuery = ""
					m.searchInput.SetValue("")
					m.UpdateListItems()
				} else {
					// Start new search
					m.searchActive = true
					m.searchInput.Focus()
				}
				return m, nil
			}

			// Tab switching
			if key.Matches(msg, m.keys.Dashboard.TabQueued) {
				m.activeTab = TabQueued
				m.ManualTabSwitch = true
				m.updateListTitle()
				m.UpdateListItems()
				return m, nil
			}
			if key.Matches(msg, m.keys.Dashboard.TabActive) {
				m.activeTab = TabActive
				m.ManualTabSwitch = true
				m.updateListTitle()
				m.UpdateListItems()
				return m, nil
			}
			if key.Matches(msg, m.keys.Dashboard.TabDone) {
				m.activeTab = TabDone
				m.ManualTabSwitch = true
				m.updateListTitle()
				m.UpdateListItems()
				return m, nil
			}
			// Quit
			if key.Matches(msg, m.keys.Dashboard.Quit) {
				// Graceful shutdown: pause all active downloads to save state
				m.Pool.GracefulShutdown()
				return m, tea.Quit
			}
			if key.Matches(msg, m.keys.Dashboard.ForceQuit) {
				m.Pool.PauseAll()
				return m, tea.Quit
			}

			// Add download
			if key.Matches(msg, m.keys.Dashboard.Add) {
				m.state = InputState
				m.focusedInput = 0
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

				// Check clipboard for URL if setting is enabled
				if m.Settings.General.ClipboardMonitor {
					if url := clipboard.ReadURL(); url != "" {
						m.inputs[0].SetValue(url)
					} else {
						m.inputs[0].SetValue("")
					}
				} else {
					m.inputs[0].SetValue("")
				}
				return m, nil
			}

			// Next Tab
			if key.Matches(msg, m.keys.Dashboard.NextTab) {
				m.activeTab = (m.activeTab + 1) % 3
				m.ManualTabSwitch = true
				m.updateListTitle()
				m.UpdateListItems()
				return m, nil
			}

			// Delete download
			if key.Matches(msg, m.keys.Dashboard.Delete) {
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
						if dl.URL != "" && dl.Destination != "" {
							_ = state.DeleteState(dl.ID, dl.URL, dl.Destination)
						}

						// Delete partial/incomplete files (only for non-completed downloads)
						if !dl.done && dl.Destination != "" {
							// Delete the .surge partial file with retries
							// (worker may still hold file briefly after Cancel on Windows)
							surgeFile := dl.Destination + types.IncompleteSuffix
							for i := 0; i < 5; i++ {
								if err := os.Remove(surgeFile); err == nil {
									break
								}
								time.Sleep(50 * time.Millisecond)
							}
						}

						// Remove completed downloads from master list (for Done tab persistence)
						if dl.done && dl.URL != "" {
							_ = state.RemoveFromMasterList(dl.ID)
						}

						// Remove from list
						m.downloads = append(m.downloads[:realIdx], m.downloads[realIdx+1:]...)
					}
					m.UpdateListItems()
					return m, nil
				}
			}

			// History
			if key.Matches(msg, m.keys.Dashboard.History) {
				// Open history view
				if entries, err := state.LoadCompletedDownloads(); err == nil {
					m.historyEntries = entries
					m.historyCursor = 0
					m.state = HistoryState
				}
				return m, nil
			}

			// Pause/Resume toggle - get selected download from list
			if key.Matches(msg, m.keys.Dashboard.Pause) {
				if d := m.GetSelectedDownload(); d != nil {
					if !d.done {
						if d.paused {
							// Resume: create config and add to pool
							d.paused = false
							d.state.Resume()
							// Use the download's actual destination directory
							outputPath := filepath.Dir(d.Destination)
							if outputPath == "" || outputPath == "." {
								outputPath = m.Settings.General.DefaultDownloadDir
								if outputPath == "" {
									outputPath = m.PWD
								}
							}
							cfg := types.DownloadConfig{
								URL:        d.URL,
								OutputPath: outputPath,
								DestPath:   d.Destination, // Full path for state lookup
								ID:         d.ID,
								Filename:   d.Filename,
								Verbose:    false,
								IsResume:   true, // Explicit resume - use saved state
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

			// Toggle log focus
			if key.Matches(msg, m.keys.Dashboard.Log) {
				m.logFocused = !m.logFocused
				return m, nil
			}

			// Open settings
			if key.Matches(msg, m.keys.Dashboard.Settings) {
				m.state = SettingsState
				m.SettingsActiveTab = 0
				m.SettingsSelectedRow = 0
				m.SettingsIsEditing = false
				return m, nil
			}

			// Batch import
			if key.Matches(msg, m.keys.Dashboard.BatchImport) {
				m.state = BatchFilePickerState
				m.filepicker = newFilepicker(m.PWD)
				m.filepicker.FileAllowed = true
				m.filepicker.DirAllowed = false
				return m, m.filepicker.Init()
			}

			// If log is focused, handle viewport scrolling
			if m.logFocused {
				if key.Matches(msg, m.keys.Dashboard.LogClose) {
					m.logFocused = false
					return m, nil
				}
				if key.Matches(msg, m.keys.Dashboard.LogDown) {
					m.logViewport.LineDown(1)
					return m, nil
				}
				if key.Matches(msg, m.keys.Dashboard.LogUp) {
					m.logViewport.LineUp(1)
					return m, nil
				}
				if key.Matches(msg, m.keys.Dashboard.LogTop) {
					m.logViewport.GotoTop()
					return m, nil
				}
				if key.Matches(msg, m.keys.Dashboard.LogBottom) {
					m.logViewport.GotoBottom()
					return m, nil
				}
				return m, nil
			}

			// Block bare ESC from propagating (only quit via ctrl+q/ctrl+c)
			if msg.String() == "esc" {
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
			if key.Matches(msg, m.keys.Input.Esc) {
				m.state = DashboardState
				return m, nil
			}
			// Tab to open file picker when on path input
			if key.Matches(msg, m.keys.Input.Tab) && m.focusedInput == 1 {
				m.state = FilePickerState
				m.filepicker = newFilepicker(m.PWD)
				return m, m.filepicker.Init()
			}
			if key.Matches(msg, m.keys.Input.Enter) {
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
					path = m.Settings.General.DefaultDownloadDir
					if path == "" {
						path = "."
					}
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
			if key.Matches(msg, m.keys.Input.Up) && m.focusedInput > 0 {
				m.inputs[m.focusedInput].Blur()
				m.focusedInput--
				m.inputs[m.focusedInput].Focus()
				return m, nil
			}
			if key.Matches(msg, m.keys.Input.Down) && m.focusedInput < 2 {
				m.inputs[m.focusedInput].Blur()
				m.focusedInput++
				m.inputs[m.focusedInput].Focus()
				return m, nil
			}

			var cmd tea.Cmd
			m.inputs[m.focusedInput], cmd = m.inputs[m.focusedInput].Update(msg)
			return m, cmd

		case FilePickerState:
			if key.Matches(msg, m.keys.FilePicker.Cancel) {
				// Cancel and return to appropriate state
				if m.SettingsFileBrowsing {
					m.SettingsFileBrowsing = false
					m.state = SettingsState
					return m, nil
				}
				m.state = InputState
				return m, nil
			}

			// H key to jump to default download directory
			if key.Matches(msg, m.keys.FilePicker.GotoHome) {
				defaultDir := m.Settings.General.DefaultDownloadDir
				if defaultDir == "" {
					homeDir, _ := os.UserHomeDir()
					defaultDir = filepath.Join(homeDir, "Downloads")
				}
				m.filepicker = newFilepicker(defaultDir)
				return m, m.filepicker.Init()
			}

			// '.' to select current directory
			if key.Matches(msg, m.keys.FilePicker.UseDir) {
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
			if key.Matches(msg, m.keys.History.Close) {
				m.state = DashboardState
				return m, nil
			}
			if key.Matches(msg, m.keys.History.Up) {
				if m.historyCursor > 0 {
					m.historyCursor--
				}
				return m, nil
			}
			if key.Matches(msg, m.keys.History.Down) {
				if m.historyCursor < len(m.historyEntries)-1 {
					m.historyCursor++
				}
				return m, nil
			}
			if key.Matches(msg, m.keys.History.Delete) {
				if m.historyCursor >= 0 && m.historyCursor < len(m.historyEntries) {
					entry := m.historyEntries[m.historyCursor]
					_ = state.RemoveFromMasterList(entry.ID)
					m.historyEntries, _ = state.LoadCompletedDownloads()
					if m.historyCursor >= len(m.historyEntries) && m.historyCursor > 0 {
						m.historyCursor--
					}
				}
				return m, nil
			}
			return m, nil

		case DuplicateWarningState:
			if key.Matches(msg, m.keys.Duplicate.Continue) {
				// Continue anyway - startDownload handles unique filename generation
				m.state = DashboardState
				return m.startDownload(m.pendingURL, m.pendingPath, m.pendingFilename)
			}
			if key.Matches(msg, m.keys.Duplicate.Cancel) {
				// Cancel - don't add
				m.state = DashboardState
				return m, nil
			}
			if key.Matches(msg, m.keys.Duplicate.Focus) {
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
			if key.Matches(msg, m.keys.Extension.Yes) {
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
			if key.Matches(msg, m.keys.Extension.No) {
				// Cancelled
				m.state = DashboardState
				return m, nil
			}
			return m, nil

		case BatchFilePickerState:
			if key.Matches(msg, m.keys.FilePicker.Cancel) {
				// Reset filepicker to directory mode and return
				m.filepicker.FileAllowed = false
				m.filepicker.DirAllowed = true
				m.filepicker.AllowedTypes = nil
				m.state = DashboardState
				return m, nil
			}

			// H key to jump to default download directory
			if key.Matches(msg, m.keys.FilePicker.GotoHome) {
				defaultDir := m.Settings.General.DefaultDownloadDir
				if defaultDir == "" {
					homeDir, _ := os.UserHomeDir()
					defaultDir = filepath.Join(homeDir, "Downloads")
				}
				m.filepicker = newFilepicker(defaultDir)
				m.filepicker.FileAllowed = true
				m.filepicker.DirAllowed = false
				return m, m.filepicker.Init()
			}

			// Pass key to filepicker
			var cmd tea.Cmd
			m.filepicker, cmd = m.filepicker.Update(msg)

			// Check if a file was selected
			if didSelect, path := m.filepicker.DidSelectFile(msg); didSelect {
				// Read URLs from file
				urls, err := readURLsFromFile(path)
				if err != nil {
					m.addLogEntry(LogStyleError.Render("✖ Failed to read batch file: " + err.Error()))
					// Reset filepicker and return
					m.filepicker.FileAllowed = false
					m.filepicker.DirAllowed = true
					m.filepicker.AllowedTypes = nil
					m.state = DashboardState
					return m, nil
				}

				// Store pending URLs and show confirmation
				m.pendingBatchURLs = urls
				m.batchFilePath = path

				// Reset filepicker to directory mode
				m.filepicker.FileAllowed = false
				m.filepicker.DirAllowed = true
				m.filepicker.AllowedTypes = nil

				m.state = BatchConfirmState
				return m, nil
			}

			return m, cmd

		case BatchConfirmState:
			if key.Matches(msg, m.keys.BatchConfirm.Confirm) {
				// Add all URLs as downloads, skipping duplicates
				path := m.Settings.General.DefaultDownloadDir
				if path == "" {
					path = "."
				}

				added := 0
				skipped := 0
				for _, url := range m.pendingBatchURLs {
					// Skip duplicate URLs
					if m.checkForDuplicate(url) != nil {
						skipped++
						continue
					}
					m, _ = m.startDownload(url, path, "")
					added++
				}

				if skipped > 0 {
					m.addLogEntry(LogStyleStarted.Render(fmt.Sprintf("⬇ Added %d downloads from batch (%d duplicates skipped)", added, skipped)))
				} else {
					m.addLogEntry(LogStyleStarted.Render(fmt.Sprintf("⬇ Added %d downloads from batch", added)))
				}
				m.pendingBatchURLs = nil
				m.batchFilePath = ""
				m.state = DashboardState
				return m, nil
			}
			if key.Matches(msg, m.keys.BatchConfirm.Cancel) {
				m.pendingBatchURLs = nil
				m.batchFilePath = ""
				m.state = DashboardState
				return m, nil
			}
			return m, nil

		case SettingsState:
			// Handle editing mode first
			if m.SettingsIsEditing {
				if key.Matches(msg, m.keys.SettingsEditor.Cancel) {
					// Cancel editing
					m.SettingsIsEditing = false
					m.SettingsInput.Blur()
					return m, nil
				}
				if key.Matches(msg, m.keys.SettingsEditor.Confirm) {
					// Commit the value
					categories := config.CategoryOrder()
					currentCategory := categories[m.SettingsActiveTab]
					settingKey := m.getCurrentSettingKey()
					m.setSettingValue(currentCategory, settingKey, m.SettingsInput.Value())
					m.SettingsIsEditing = false
					m.SettingsInput.Blur()
					return m, nil
				}

				// Pass to text input
				var cmd tea.Cmd
				m.SettingsInput, cmd = m.SettingsInput.Update(msg)
				return m, cmd
			}

			// Not editing - handle navigation
			if key.Matches(msg, m.keys.Settings.Close) {
				// Save settings and exit
				_ = config.SaveSettings(m.Settings)
				m.state = DashboardState
				return m, nil
			}
			if key.Matches(msg, m.keys.Settings.Tab1) {
				m.SettingsActiveTab = 0
				m.SettingsSelectedRow = 0
				return m, nil
			}
			if key.Matches(msg, m.keys.Settings.Tab2) {
				m.SettingsActiveTab = 1
				m.SettingsSelectedRow = 0
				return m, nil
			}
			if key.Matches(msg, m.keys.Settings.Tab3) {
				m.SettingsActiveTab = 2
				m.SettingsSelectedRow = 0
				return m, nil
			}
			if key.Matches(msg, m.keys.Settings.Tab4) {
				m.SettingsActiveTab = 3
				m.SettingsSelectedRow = 0
				return m, nil
			}

			// Tab Navigation
			if key.Matches(msg, m.keys.Settings.NextTab) {
				m.SettingsActiveTab = (m.SettingsActiveTab + 1) % 4
				m.SettingsSelectedRow = 0
				return m, nil
			}
			if key.Matches(msg, m.keys.Settings.PrevTab) {
				m.SettingsActiveTab = (m.SettingsActiveTab - 1 + 4) % 4
				m.SettingsSelectedRow = 0
				return m, nil
			}

			// Open file browser for default_download_dir
			if key.Matches(msg, m.keys.Settings.Browse) {
				settingKey := m.getCurrentSettingKey()
				if settingKey == "default_download_dir" {
					m.SettingsFileBrowsing = true
					m.state = FilePickerState
					m.filepicker = newFilepicker(m.Settings.General.DefaultDownloadDir)
					return m, m.filepicker.Init()
				}
				return m, nil
			}

			// Back tab - not currently bound, ignoring or could use Shift+Tab manual check if really needed
			// For now, we rely on Tab (Browse) to cycle.

			// Up/Down navigation
			if key.Matches(msg, m.keys.Settings.Up) {
				if m.SettingsSelectedRow > 0 {
					m.SettingsSelectedRow--
				}
				return m, nil
			}
			if key.Matches(msg, m.keys.Settings.Down) {
				maxRow := m.getSettingsCount() - 1
				if m.SettingsSelectedRow < maxRow {
					m.SettingsSelectedRow++
				}
				return m, nil
			}

			// Edit / Toggle
			if key.Matches(msg, m.keys.Settings.Edit) {
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
			}

			// Reset
			if key.Matches(msg, m.keys.Settings.Reset) {
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

		case UpdateAvailableState:
			if key.Matches(msg, m.keys.Update.OpenGitHub) {
				// Open the release page in browser
				if m.UpdateInfo != nil && m.UpdateInfo.ReleaseURL != "" {
					_ = openBrowser(m.UpdateInfo.ReleaseURL)
				}
				m.state = DashboardState
				m.UpdateInfo = nil
				return m, nil
			}
			if key.Matches(msg, m.keys.Update.IgnoreNow) {
				// Just dismiss the modal
				m.state = DashboardState
				m.UpdateInfo = nil
				return m, nil
			}
			if key.Matches(msg, m.keys.Update.NeverRemind) {
				// Persist the setting and dismiss
				m.Settings.General.SkipUpdateCheck = true
				_ = config.SaveSettings(m.Settings)
				m.state = DashboardState
				m.UpdateInfo = nil
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
			if !d.done {
				// Check by Filename (set via DownloadStartedMsg)
				if d.Filename == name {
					return true
				}
				// Also check by Destination path basename (set earlier, more reliable)
				if d.Destination != "" && filepath.Base(d.Destination) == name {
					return true
				}
			}
		}
		return false
	}

	// Check if file exists on disk (including incomplete .surge files)
	existsOnDisk := func(name string) bool {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			return true
		}
		// Also check for incomplete download file (.surge extension)
		if _, err := os.Stat(path + types.IncompleteSuffix); !os.IsNotExist(err) {
			return true
		}
		return false
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

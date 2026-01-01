package tui

import (
	"time"

	"surge/internal/downloader"
	"surge/internal/messages"
	"surge/internal/utils"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
)

// Update handles messages and updates the model
func (m RootModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case StartDownloadMsg:
		// Handle download request from HTTP server
		path := msg.Path
		if path == "" {
			path = "."
		}

		nextID := len(m.downloads) + 1
		newDownload := NewDownloadModel(nextID, msg.URL, "Queued", 0)
		m.downloads = append(m.downloads, newDownload)

		cfg := downloader.DownloadConfig{
			URL:        msg.URL,
			OutputPath: path,
			ID:         nextID,
			Verbose:    false,
			ProgressCh: m.progressChan,
			State:      newDownload.state,
		}

		utils.Debug("Adding download from server: %s", msg.URL)
		m.Pool.Add(cfg)
		return m, nil

	case messages.DownloadStartedMsg:
		// Find the download and update with real metadata + start polling
		for _, d := range m.downloads {
			if d.ID == msg.DownloadID {
				d.Filename = msg.Filename
				d.Total = msg.Total
				d.URL = msg.URL
				// Update the progress state with real total size
				d.state.SetTotalSize(msg.Total)
				// Start polling for this download
				cmds = append(cmds, d.reporter.PollCmd())
				break
			}
		}
		cmds = append(cmds, listenForActivity(m.progressChan))

	case messages.ProgressMsg:
		// Progress from polling reporter
		for _, d := range m.downloads {
			if d.ID == msg.DownloadID {
				// Don't update if already done
				if d.done {
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
				// Continue polling only if not done
				if !d.done {
					cmds = append(cmds, d.reporter.PollCmd())
				}
				break
			}
		}

	case messages.DownloadCompleteMsg:
		for _, d := range m.downloads {
			if d.ID == msg.DownloadID {
				d.Downloaded = d.Total
				d.Elapsed = msg.Elapsed
				d.done = true
				// Set progress to 100%
				cmds = append(cmds, d.progress.SetPercent(1.0))
				break
			}
		}
		cmds = append(cmds, listenForActivity(m.progressChan))

	case messages.DownloadErrorMsg:
		for _, d := range m.downloads {
			if d.ID == msg.DownloadID {
				d.err = msg.Err
				d.done = true
				break
			}
		}
		cmds = append(cmds, listenForActivity(m.progressChan))

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch m.state {
		case DashboardState:
			if msg.String() == "q" || msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
			if msg.String() == "g" {
				m.state = InputState
				m.focusedInput = 0
				m.inputs[0].SetValue("")
				m.inputs[0].Focus()
				m.inputs[1].SetValue(".")
				m.inputs[1].Blur()
				m.inputs[2].SetValue("")
				m.inputs[2].Blur()
				return m, nil
			}

			// Navigation
			if msg.String() == "up" || msg.String() == "k" {
				if m.cursor > 0 {
					m.cursor--
				}
			}
			if msg.String() == "down" || msg.String() == "j" {
				if m.cursor < len(m.downloads)-1 {
					m.cursor++
				}
			}

			// Details
			if msg.String() == "enter" {
				if len(m.downloads) > 0 {
					m.state = DetailState
				}
			}

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
				// filename := m.inputs[2].Value() // Will use later
				m.state = DashboardState

				// Create download with state and reporter
				nextID := len(m.downloads) + 1 // FIXME: proper UUIDs? This could lead to unexpected problems!!!
				newDownload := NewDownloadModel(nextID, url, "Queued", 0)
				m.downloads = append(m.downloads, newDownload)

				// Create config
				cfg := downloader.DownloadConfig{
					URL:        url,
					OutputPath: path,
					ID:         nextID,
					Verbose:    false,
					ProgressCh: m.progressChan,
					State:      newDownload.state,
				}

				utils.Debug("Adding to Queue")
				m.Pool.Add(cfg)

				return m, nil
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
		}
	}

	// Propagate messages to progress bars
	for i := range m.downloads {
		var cmd tea.Cmd
		var newModel tea.Model
		newModel, cmd = m.downloads[i].progress.Update(msg)
		if p, ok := newModel.(progress.Model); ok {
			m.downloads[i].progress = p
		}
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

package tui

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/surge-downloader/surge/internal/config"
	"github.com/surge-downloader/surge/internal/download"
	"github.com/surge-downloader/surge/internal/engine/state"
	"github.com/surge-downloader/surge/internal/engine/types"
	"github.com/surge-downloader/surge/internal/version"
)

type UIState int //Defines UIState as int to be used in rootModel

const (
	DashboardState             UIState = iota //DashboardState is 0 increments after each line
	InputState                                //InputState is 1
	DetailState                               //DetailState is 2
	FilePickerState                           //FilePickerState is 3
	HistoryState                              //HistoryState is 4
	DuplicateWarningState                     //DuplicateWarningState is 5
	SearchState                               //SearchState is 6
	SettingsState                             //SettingsState is 7
	ExtensionConfirmationState                //ExtensionConfirmationState is 8
	BatchFilePickerState                      //BatchFilePickerState is 9
	BatchConfirmState                         //BatchConfirmState is 10
	UpdateAvailableState                      //UpdateAvailableState is 11
)

const (
	TabQueued = 0
	TabActive = 1
	TabDone   = 2
)

type DownloadModel struct {
	ID          string
	URL         string
	Filename    string
	Destination string // Full path to the destination file
	Total       int64
	Downloaded  int64
	Speed       float64
	Connections int

	StartTime time.Time
	Elapsed   time.Duration

	progress progress.Model

	// Hybrid architecture: atomic state + polling reporter
	state    *types.ProgressState
	reporter *ProgressReporter

	done   bool
	err    error
	paused bool
}

type RootModel struct {
	downloads    []*DownloadModel
	width        int
	height       int
	state        UIState
	activeTab    int // 0=Queued, 1=Active, 2=Done
	inputs       []textinput.Model
	focusedInput int
	progressChan chan any // Channel for events only (start/complete/error)

	// File picker for directory selection
	filepicker filepicker.Model

	// Bubbles help component
	help help.Model

	// Bubbles list component for download listing
	list list.Model

	Pool *download.WorkerPool //Works as the download queue
	PWD  string

	// History view
	historyEntries []types.DownloadEntry
	historyCursor  int

	// Duplicate detection
	pendingURL      string // URL pending confirmation
	pendingPath     string // Path pending confirmation
	pendingFilename string // Filename pending confirmation
	duplicateInfo   string // Info about the duplicate

	// Graph Data
	SpeedHistory           []float64 // Stores the last ~60 ticks of speed data
	lastSpeedHistoryUpdate time.Time // Last time SpeedHistory was updated (for 0.5s sampling)
	speedBuffer            []float64 // Buffer for rolling average (last 10 speed readings)

	// Notification log system
	logViewport viewport.Model // Scrollable log viewport
	logEntries  []string       // Log entries for download events
	logFocused  bool           // Whether the log viewport is focused

	// Settings
	Settings             *config.Settings // Application settings
	SettingsActiveTab    int              // Active category tab (0-3)
	SettingsSelectedRow  int              // Selected setting within current tab
	SettingsIsEditing    bool             // Whether currently editing a value
	SettingsInput        textinput.Model  // Input for editing string/int values
	SettingsFileBrowsing bool             // Whether browsing for a directory

	// Selection persistence
	SelectedDownloadID string // ID of the currently selected download
	ManualTabSwitch    bool   // Whether the last tab switch was manual

	// Search functionality
	searchInput  textinput.Model // Text input for search
	searchActive bool            // Whether search mode is active
	searchQuery  string          // Current search query

	// Batch import
	pendingBatchURLs []string // URLs pending batch import
	batchFilePath    string   // Path to the batch file

	// Keybindings
	keys KeyMap

	// Server port for display
	ServerPort int

	// Update check
	UpdateInfo     *version.UpdateInfo // Update information (nil if no update available)
	CurrentVersion string              // Current version of Surge
}

// NewDownloadModel creates a new download model with progress state and reporter
func NewDownloadModel(id string, url string, filename string, total int64) *DownloadModel {
	state := types.NewProgressState(id, total)
	return &DownloadModel{
		ID:        id,
		URL:       url,
		Filename:  filename,
		Total:     total,
		StartTime: time.Now(),
		progress:  progress.New(progress.WithSpringOptions(0.5, 0.1)),
		state:     state,
		reporter:  NewProgressReporter(state),
	}
}

func InitialRootModel(serverPort int, currentVersion string, pool *download.WorkerPool, progressChan chan any) RootModel {
	// Initialize inputs
	urlInput := textinput.New()
	urlInput.Placeholder = "https://example.com/file.zip"
	urlInput.Focus()
	urlInput.Width = InputWidth
	urlInput.Prompt = ""

	pathInput := textinput.New()
	pathInput.Placeholder = "."
	pathInput.Width = InputWidth
	pathInput.Prompt = ""
	pathInput.SetValue(".")

	filenameInput := textinput.New()
	filenameInput.Placeholder = "(auto-detect)"
	filenameInput.Width = InputWidth
	filenameInput.Prompt = ""

	pwd, _ := os.Getwd()

	// Initialize file picker for directory selection - default to Downloads folder
	homeDir, _ := os.UserHomeDir()
	downloadsDir := filepath.Join(homeDir, "Downloads")
	fp := filepicker.New()
	fp.CurrentDirectory = downloadsDir
	fp.DirAllowed = true
	fp.FileAllowed = false
	fp.ShowHidden = false
	fp.ShowSize = true
	fp.ShowPermissions = true
	fp.SetHeight(FilePickerHeight)

	// Load paused downloads from master list (now uses global config directory)
	var downloads []*DownloadModel
	if pausedEntries, err := state.LoadPausedDownloads(); err == nil {
		for _, entry := range pausedEntries {
			var id string
			if entry.ID != "" {
				id = entry.ID
			}
			dm := NewDownloadModel(id, entry.URL, entry.Filename, 0)
			dm.paused = true
			dm.Destination = entry.DestPath // Store destination for state lookup on resume
			// Load actual progress from state file (using URL+DestPath for unique lookup)
			if state, err := state.LoadState(entry.URL, entry.DestPath); err == nil {
				dm.Downloaded = state.Downloaded
				dm.Total = state.TotalSize
				dm.state.Downloaded.Store(state.Downloaded)
				dm.state.SetTotalSize(state.TotalSize)
				// Set progress bar to correct position
				if state.TotalSize > 0 {
					dm.progress.SetPercent(float64(state.Downloaded) / float64(state.TotalSize))
				}
			}
			downloads = append(downloads, dm)
		}
	}

	// Load completed downloads from master list (for Done tab persistence)
	if completedEntries, err := state.LoadCompletedDownloads(); err == nil {
		for _, entry := range completedEntries {
			var id string
			if entry.ID != "" {
				id = entry.ID
			}
			dm := NewDownloadModel(id, entry.URL, entry.Filename, entry.TotalSize)
			dm.done = true
			dm.Destination = entry.DestPath
			dm.Elapsed = time.Duration(entry.TimeTaken) * time.Millisecond
			dm.Downloaded = entry.TotalSize
			dm.progress.SetPercent(1.0)
			downloads = append(downloads, dm)
		}
	}

	// Initialize the download list
	downloadList := NewDownloadList(80, 20) // Default size, will be resized on WindowSizeMsg

	// Initialize help
	helpModel := help.New()
	helpModel.Styles.ShortKey = lipgloss.NewStyle().Foreground(ColorLightGray)
	helpModel.Styles.ShortDesc = lipgloss.NewStyle().Foreground(ColorGray)

	// Load settings from disk (or defaults)
	settings, _ := config.LoadSettings()

	// Initialize settings input for editing
	settingsInput := textinput.New()
	settingsInput.Width = 40
	settingsInput.Prompt = ""

	// Initialize search input
	searchInput := textinput.New()
	searchInput.Placeholder = "Type to search..."
	searchInput.Width = 30
	searchInput.Prompt = ""

	return RootModel{
		downloads:      downloads,
		inputs:         []textinput.Model{urlInput, pathInput, filenameInput},
		state:          DashboardState,
		progressChan:   progressChan,
		filepicker:     fp,
		help:           helpModel,
		list:           downloadList,
		Pool:           pool,
		PWD:            pwd,
		SpeedHistory:   make([]float64, GraphHistoryPoints), // 60 points of history (30s at 0.5s interval)
		logViewport:    viewport.New(40, 5),                 // Default size, will be resized
		logEntries:     make([]string, 0),
		Settings:       settings,
		SettingsInput:  settingsInput,
		searchInput:    searchInput,
		keys:           Keys,
		ServerPort:     serverPort,
		CurrentVersion: currentVersion,
	}
}

func (m RootModel) Init() tea.Cmd {
	// Trigger update check if not disabled in settings
	if !m.Settings.General.SkipUpdateCheck {
		return checkForUpdateCmd(m.CurrentVersion)
	}
	return nil
}

// Helper to get downloads for the current tab
func (m RootModel) getFilteredDownloads() []*DownloadModel {
	var filtered []*DownloadModel
	searchLower := strings.ToLower(m.searchQuery)

	for _, d := range m.downloads {
		// Apply tab filter first
		switch m.activeTab {
		case TabQueued:
			if d.done || d.Speed > 0 {
				continue
			}
		case TabActive:
			if d.done || (d.Speed == 0 && d.Connections == 0) {
				continue
			}
		case TabDone:
			if !d.done {
				continue
			}
		}

		// Apply search filter if query is set
		if m.searchQuery != "" {
			if !strings.Contains(strings.ToLower(d.Filename), searchLower) {
				continue
			}
		}

		filtered = append(filtered, d)
	}
	return filtered
}

// resetFilepicker resets the filepicker to default directory-only mode
func (m *RootModel) resetFilepicker() {
	m.filepicker.FileAllowed = false
	m.filepicker.DirAllowed = true
	m.filepicker.AllowedTypes = nil
}

// newFilepicker creates a fresh filepicker instance with consistent settings.
// This is necessary to avoid cursor desync issues that cause "index out of range"
// panics when navigating directories (especially on Windows).
// See: https://github.com/charmbracelet/bubbles/issues/864
func newFilepicker(currentDir string) filepicker.Model {
	fp := filepicker.New()
	fp.CurrentDirectory = currentDir
	fp.DirAllowed = true
	fp.FileAllowed = false
	fp.ShowHidden = false
	fp.ShowSize = true
	fp.ShowPermissions = true
	fp.SetHeight(FilePickerHeight)
	return fp
}

package tui

import (
	"os"
	"path/filepath"
	"time"

	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"surge/internal/downloader"
)

type UIState int //Defines UIState as int to be used in rootModel

const (
	DashboardState        UIState = iota //DashboardState is 0 increments after each line
	InputState                           //InputState is 1
	DetailState                          //DetailState is 2
	FilePickerState                      //FilePickerState is 3
	HistoryState                         //HistoryState is 4
	DuplicateWarningState                //DuplicateWarningState is 5
)

// StartDownloadMsg is sent from the HTTP server to start a new download
type StartDownloadMsg struct {
	URL      string
	Path     string
	Filename string
}

type DownloadModel struct {
	ID          int
	URL         string
	Filename    string
	Total       int64
	Downloaded  int64
	Speed       float64
	Connections int

	StartTime time.Time
	Elapsed   time.Duration

	progress progress.Model

	// Hybrid architecture: atomic state + polling reporter
	state    *downloader.ProgressState
	reporter *ProgressReporter

	done   bool
	err    error
	paused bool
}

type RootModel struct {
	downloads      []*DownloadModel
	NextDownloadID int // Monotonic counter for unique download IDs
	width          int
	height         int
	state          UIState
	inputs         []textinput.Model
	focusedInput   int
	progressChan   chan tea.Msg // Channel for events only (start/complete/error)

	// File picker for directory selection
	filepicker filepicker.Model

	// Navigation
	cursor       int
	scrollOffset int // First visible download index for viewport scrolling

	Pool *downloader.WorkerPool //Works as the download queue
	PWD  string

	// History view
	historyEntries []downloader.DownloadEntry
	historyCursor  int

	// Duplicate detection
	pendingURL      string // URL pending confirmation
	pendingPath     string // Path pending confirmation
	pendingFilename string // Filename pending confirmation
	duplicateInfo   string // Info about the duplicate
}

// NewDownloadModel creates a new download model with progress state and reporter
func NewDownloadModel(id int, url string, filename string, total int64) *DownloadModel {
	state := downloader.NewProgressState(id, total)
	return &DownloadModel{
		ID:        id,
		URL:       url,
		Filename:  filename,
		Total:     total,
		StartTime: time.Now(),
		progress:  progress.New(progress.WithDefaultGradient()),
		state:     state,
		reporter:  NewProgressReporter(state),
	}
}

func InitialRootModel() RootModel {
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

	// Create channel first so we can pass it to WorkerPool
	progressChan := make(chan tea.Msg, ProgressChannelBuffer)

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
	if pausedEntries, err := downloader.LoadPausedDownloads(); err == nil {
		for i, entry := range pausedEntries {
			id := i + 1 // Assign sequential IDs
			dm := NewDownloadModel(id, entry.URL, entry.Filename, 0)
			dm.paused = true
			// Load actual progress from state file
			if state, err := downloader.LoadState(entry.URL); err == nil {
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

	return RootModel{
		downloads:      downloads,
		NextDownloadID: len(downloads) + 1, // Start after loaded downloads
		inputs:         []textinput.Model{urlInput, pathInput, filenameInput},
		state:          DashboardState,
		progressChan:   progressChan,
		filepicker:     fp,
		Pool:           downloader.NewWorkerPool(progressChan),
		PWD:            pwd,
	}
}

func (m RootModel) Init() tea.Cmd {
	return listenForActivity(m.progressChan)
}

// getVisibleCount returns how many download cards can fit in the current terminal height
func (m RootModel) getVisibleCount() int {
	availableHeight := m.height - HeaderHeight - 2 // Reserve space for footer
	visibleCount := availableHeight / CardHeight
	if visibleCount < 1 {
		visibleCount = 1
	}
	if visibleCount > len(m.downloads) {
		visibleCount = len(m.downloads)
	}
	return visibleCount
}

func listenForActivity(sub chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-sub
	}
}

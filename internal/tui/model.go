package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"surge/internal/downloader"
)

type UIState int //Defines UIState as int to be used in rootModel

const (
	DashboardState UIState = iota //DashboardState is 0 increments after each line
	InputState                    //InputState is 1
	DetailState                   //DetailState is 2
)

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

	done bool
	err  error
}

type RootModel struct {
	downloads    []*DownloadModel
	width        int
	height       int
	state        UIState
	inputs       []textinput.Model
	focusedInput int
	progressChan chan tea.Msg // Channel for events only (start/complete/error)

	// Navigation
	cursor int

	Pool *downloader.WorkerPool //Works as the download queue
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

	return RootModel{
		downloads:    make([]*DownloadModel, 0),
		inputs:       []textinput.Model{urlInput, pathInput, filenameInput},
		state:        DashboardState,
		progressChan: progressChan,
		Pool:         downloader.NewWorkerPool(progressChan),
	}
}

func (m RootModel) Init() tea.Cmd {
	return listenForActivity(m.progressChan)
}

func listenForActivity(sub chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-sub
	}
}

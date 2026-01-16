package downloader

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/surge-downloader/surge/internal/utils"

	tea "github.com/charmbracelet/bubbletea"
)

// Buffer pool to reduce GC pressure
var bufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, WorkerBuffer)
		return &buf
	},
}

// ConcurrentDownloader handles multi-connection downloads
type ConcurrentDownloader struct {
	ProgressChan chan<- tea.Msg // Channel for events (start/complete/error)
	ID           string         // Download ID
	State        *ProgressState // Shared state for TUI polling
	activeTasks  map[int]*ActiveTask
	activeMu     sync.Mutex
	URL          string // For pause/resume
	DestPath     string // For pause/resume
	Runtime      *RuntimeConfig
}

// NewConcurrentDownloader creates a new concurrent downloader with all required parameters
func NewConcurrentDownloader(id string, progressCh chan<- tea.Msg, state *ProgressState, runtime *RuntimeConfig) *ConcurrentDownloader {
	return &ConcurrentDownloader{
		ID:           id,
		ProgressChan: progressCh,
		State:        state,
		activeTasks:  make(map[int]*ActiveTask),
		Runtime:      runtime,
	}
}

// Task represents a byte range to download
type Task struct {
	Offset int64 // in bytes
	Length int64
}

// ActiveTask tracks a task currently being processed by a worker
type ActiveTask struct {
	Task          Task
	CurrentOffset int64 // Atomic
	StopAt        int64 // Atomic

	// Health monitoring fields
	LastActivity int64              // Atomic: Unix nano timestamp of last data received
	Speed        float64            // EMA-smoothed speed in bytes/sec (protected by mutex)
	StartTime    time.Time          // When this task started
	Cancel       context.CancelFunc // Cancel function to abort this task
	SpeedMu      sync.Mutex         // Protects Speed field

	// Sliding window for recent speed tracking
	WindowStart time.Time // When current measurement window started
	WindowBytes int64     // Bytes downloaded in current window (atomic)
}

// RemainingBytes returns the number of bytes left for this task
func (at *ActiveTask) RemainingBytes() int64 {
	current := atomic.LoadInt64(&at.CurrentOffset)
	stopAt := atomic.LoadInt64(&at.StopAt)
	if current >= stopAt {
		return 0
	}
	return stopAt - current
}

// RemainingTask returns a Task representing the remaining work, or nil if complete
func (at *ActiveTask) RemainingTask() *Task {
	current := atomic.LoadInt64(&at.CurrentOffset)
	stopAt := atomic.LoadInt64(&at.StopAt)
	if current >= stopAt {
		return nil
	}
	return &Task{Offset: current, Length: stopAt - current}
}

// GetSpeed returns the current EMA-smoothed speed
func (at *ActiveTask) GetSpeed() float64 {
	at.SpeedMu.Lock()
	defer at.SpeedMu.Unlock()
	return at.Speed
}

// alignedSplitSize calculates a split size that is half of remaining, aligned to AlignSize
// Returns 0 if the split would be smaller than MinChunk
func alignedSplitSize(remaining int64) int64 {
	half := (remaining / 2 / AlignSize) * AlignSize
	if half < MinChunk {
		return 0
	}
	return half
}

// TaskQueue is a thread-safe work-stealing queue
type TaskQueue struct {
	tasks       []Task
	head        int
	mu          sync.Mutex
	cond        *sync.Cond
	done        bool
	idleWorkers int64 // Atomic counter for idle workers
}

func NewTaskQueue() *TaskQueue {
	tq := &TaskQueue{}
	tq.cond = sync.NewCond(&tq.mu)
	return tq
}

func (q *TaskQueue) Push(t Task) {
	q.mu.Lock()
	q.tasks = append(q.tasks, t)
	q.cond.Signal()
	q.mu.Unlock()
}

func (q *TaskQueue) PushMultiple(tasks []Task) {
	q.mu.Lock()
	q.tasks = append(q.tasks, tasks...)
	q.cond.Broadcast()
	q.mu.Unlock()
}

func (q *TaskQueue) Pop() (Task, bool) {
	// Mark as idle while waiting
	atomic.AddInt64(&q.idleWorkers, 1)

	q.mu.Lock()
	defer q.mu.Unlock()

	for len(q.tasks) == 0 && !q.done {
		q.cond.Wait()
	}

	// No longer idle once we have work (or are done)
	atomic.AddInt64(&q.idleWorkers, -1)

	if len(q.tasks) == 0 {
		return Task{}, false
	}

	t := q.tasks[q.head]
	q.head++
	if q.head > len(q.tasks)/2 {
		q.tasks = append([]Task(nil), q.tasks[q.head:]...)
		q.head = 0
	}
	return t, true
}

func (q *TaskQueue) Close() {
	q.mu.Lock()
	q.done = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

func (q *TaskQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.tasks) - q.head
}

func (q *TaskQueue) IdleWorkers() int64 {
	return atomic.LoadInt64(&q.idleWorkers)
}

// DrainRemaining returns all remaining tasks in the queue (used for pause/resume)
func (q *TaskQueue) DrainRemaining() []Task {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.head >= len(q.tasks) {
		return nil
	}

	remaining := make([]Task, len(q.tasks)-q.head)
	copy(remaining, q.tasks[q.head:])
	q.tasks = nil
	q.head = 0
	return remaining
}

// SplitLargestIfNeeded finds the largest queued task and splits it if > 2*MinChunk
// Returns true if a split occurred
func (q *TaskQueue) SplitLargestIfNeeded() bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Find largest queued task
	idx := -1
	var maxLen int64 = 0
	for i, t := range q.tasks {
		if t.Length > maxLen && t.Length > 2*MinChunk {
			maxLen = t.Length
			idx = i
		}
	}

	if idx == -1 {
		return false // No task large enough to split
	}

	t := q.tasks[idx]

	// Split in half, aligned to AlignSize
	half := alignedSplitSize(t.Length)
	if half == 0 {
		return false // Halves would be too small
	}

	left := Task{Offset: t.Offset, Length: half}
	right := Task{Offset: t.Offset + half, Length: t.Length - half}

	// Replace original with right half, append left half
	q.tasks[idx] = right
	q.tasks = append(q.tasks, left)

	// Wake up idle workers
	q.cond.Broadcast()
	return true
}

// getInitialConnections returns the starting number of connections based on file size
func (d *ConcurrentDownloader) getInitialConnections(fileSize int64) int {
	maxConns := d.Runtime.GetMaxConnectionsPerHost()

	var recConns int
	switch {
	case fileSize < 10*MB:
		recConns = 1
	case fileSize < 100*MB:
		recConns = 4
	case fileSize < 1*GB:
		recConns = 6
	default:
		recConns = 32
	}

	if recConns > maxConns {
		return maxConns
	}
	return recConns
}

// calculateChunkSize determines optimal chunk size
func (d *ConcurrentDownloader) calculateChunkSize(fileSize int64, numConns int) int64 {
	targetChunks := int64(numConns * TasksPerWorker)
	chunkSize := fileSize / targetChunks

	// Clamp to min/max from config
	minChunk := d.Runtime.GetMinChunkSize()
	maxChunk := d.Runtime.GetMaxChunkSize()
	targetChunk := d.Runtime.GetTargetChunkSize()

	// If calculating produces something wild, prefer target
	if chunkSize == 0 {
		chunkSize = targetChunk
	}

	if chunkSize < minChunk {
		chunkSize = minChunk
	}
	if chunkSize > maxChunk {
		chunkSize = maxChunk
	}

	// Align to 4KB
	chunkSize = (chunkSize / AlignSize) * AlignSize
	if chunkSize == 0 {
		chunkSize = AlignSize
	}

	return chunkSize
}

// createTasks generates initial task queue from file size and chunk size
func createTasks(fileSize, chunkSize int64) []Task {
	if chunkSize <= 0 {
		return nil
	}
	var tasks []Task
	for offset := int64(0); offset < fileSize; offset += chunkSize {
		length := chunkSize
		if offset+length > fileSize {
			length = fileSize - offset
		}
		tasks = append(tasks, Task{Offset: offset, Length: length})
	}
	return tasks
}

// newConcurrentClient creates an http.Client tuned for concurrent downloads
func (d *ConcurrentDownloader) newConcurrentClient(numConns int) *http.Client {
	// Ensure we have enough connections per host
	maxConns := d.Runtime.GetMaxConnectionsPerHost()
	if numConns > maxConns {
		maxConns = numConns
	}

	transport := &http.Transport{
		// Connection pooling
		MaxIdleConns:        DefaultMaxIdleConns,
		MaxIdleConnsPerHost: maxConns + 2, // Slightly more than max to handle bursts
		MaxConnsPerHost:     maxConns,

		// Timeouts to prevent hung connections
		IdleConnTimeout:       DefaultIdleConnTimeout,
		TLSHandshakeTimeout:   DefaultTLSHandshakeTimeout,
		ResponseHeaderTimeout: DefaultResponseHeaderTimeout,
		ExpectContinueTimeout: DefaultExpectContinueTimeout,

		// Performance tuning
		DisableCompression: true,  // Files are usually already compressed
		ForceAttemptHTTP2:  false, // FORCE HTTP/1.1 for multiple TCP connections
		TLSNextProto:       make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),

		// Dial settings for TCP reliability
		DialContext: (&net.Dialer{
			Timeout:   DialTimeout,
			KeepAlive: KeepAliveDuration,
		}).DialContext,
	}

	return &http.Client{
		Transport: transport,
	}
}

// IncompleteSuffix is appended to files while downloading
const IncompleteSuffix = ".surge"

// Download downloads a file using multiple concurrent connections
// Uses pre-probed metadata (file size already known)
func (d *ConcurrentDownloader) Download(ctx context.Context, rawurl, destPath string, fileSize int64, verbose bool) error {
	utils.Debug("ConcurrentDownloader.Download: %s -> %s (size: %d)", rawurl, destPath, fileSize)

	// Store URL and path for pause/resume (final path without .surge)
	d.URL = rawurl
	d.DestPath = destPath

	// Working file has .surge suffix until download completes
	workingPath := destPath + IncompleteSuffix

	// Create cancellable context for pause support
	downloadCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if d.State != nil {
		d.State.CancelFunc = cancel
	}

	// Determine connections and chunk size
	numConns := d.getInitialConnections(fileSize)
	chunkSize := d.calculateChunkSize(fileSize, numConns)

	// Create tuned HTTP client for concurrent downloads
	client := d.newConcurrentClient(numConns)

	if verbose {
		fmt.Printf("File size: %s, connections: %d, chunk size: %s\n",
			utils.ConvertBytesToHumanReadable(fileSize),
			numConns,
			utils.ConvertBytesToHumanReadable(chunkSize))
	}

	// Create and preallocate output file with .surge suffix
	outFile, err := os.OpenFile(workingPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer outFile.Close()

	// Check for saved state BEFORE truncating (resume case)
	var tasks []Task
	savedState, err := LoadState(rawurl, destPath)
	isResume := err == nil && savedState != nil && len(savedState.Tasks) > 0

	if isResume {
		// Resume: use saved tasks and restore downloaded counter
		tasks = savedState.Tasks
		if d.State != nil {
			d.State.Downloaded.Store(savedState.Downloaded)
		}
		utils.Debug("Resuming from saved state: %d tasks, %d bytes downloaded", len(tasks), savedState.Downloaded)
	} else {
		// Fresh download: preallocate file and create new tasks
		if err := outFile.Truncate(fileSize); err != nil {
			return fmt.Errorf("failed to preallocate file: %w", err)
		}
		tasks = createTasks(fileSize, chunkSize)
	}
	queue := NewTaskQueue()
	queue.PushMultiple(tasks)

	// Start time for stats
	startTime := time.Now()

	// Start balancer goroutine for dynamic chunk splitting
	balancerCtx, cancelBalancer := context.WithCancel(downloadCtx)
	defer cancelBalancer()

	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		maxSplits := 50
		splitCount := 0

		for {
			select {
			case <-balancerCtx.Done():
				return
			case <-ticker.C:
				if queue.IdleWorkers() > 0 && splitCount < maxSplits {
					if queue.SplitLargestIfNeeded() {
						splitCount++
						utils.Debug("Balancer: split largest task (total splits: %d)", splitCount)
					} else if queue.Len() == 0 {
						// Try to steal from an active worker
						if d.StealWork(queue) {
							splitCount++
						}
					}
				}
			}
		}
	}()

	// Monitor for completion
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-balancerCtx.Done():
				return
			case <-ticker.C:
				if queue.Len() == 0 && int(queue.IdleWorkers()) == numConns {
					queue.Close()
					return
				}
			}
		}
	}()

	// Health monitor: detect slow workers
	go func() {
		ticker := time.NewTicker(healthCheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-balancerCtx.Done():
				return
			case <-ticker.C:
				d.checkWorkerHealth()
			}
		}
	}()

	// Start workers
	var wg sync.WaitGroup
	workerErrors := make(chan error, numConns)

	for i := 0; i < numConns; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			err := d.worker(downloadCtx, workerID, rawurl, outFile, queue, fileSize, startTime, verbose, client)
			if err != nil && err != context.Canceled {
				workerErrors <- err
			}
		}(i)
	}

	// Wait for all workers to complete
	go func() {
		wg.Wait()
		close(workerErrors)
		queue.Close()
	}()

	// Check for errors or pause
	var downloadErr error
	for err := range workerErrors {
		if err != nil {
			downloadErr = err
		}
	}

	// Handle pause: save state and exit gracefully
	if d.State != nil && d.State.IsPaused() {
		// Collect remaining tasks
		remainingTasks := queue.DrainRemaining()

		// Also collect active tasks as remaining work
		d.activeMu.Lock()
		for _, active := range d.activeTasks {
			if remaining := active.RemainingTask(); remaining != nil {
				remainingTasks = append(remainingTasks, *remaining)
			}
		}
		d.activeMu.Unlock()

		// Calculate Downloaded from remaining tasks (ensures consistency)
		var remainingBytes int64
		for _, task := range remainingTasks {
			remainingBytes += task.Length
		}
		computedDownloaded := fileSize - remainingBytes

		// Debug: compare atomic counter vs computed value to verify fix
		atomicDownloaded := d.State.Downloaded.Load()
		if atomicDownloaded != computedDownloaded {
			utils.Debug("PAUSE FIX: Atomic counter=%d, Computed from tasks=%d, Diff=%d bytes",
				atomicDownloaded, computedDownloaded, atomicDownloaded-computedDownloaded)
		}

		// Save state for resume (use computed value for consistency)
		state := &DownloadState{
			URL:        d.URL,
			ID:         d.ID,
			DestPath:   destPath,
			TotalSize:  fileSize,
			Downloaded: computedDownloaded, // FIX: Use computed value instead of atomic counter
			Tasks:      remainingTasks,
			Filename:   filepath.Base(destPath),
		}
		if err := SaveState(d.URL, destPath, state); err != nil {
			utils.Debug("Failed to save pause state: %v", err)
		}

		utils.Debug("Download paused, state saved (Downloaded=%d, RemainingTasks=%d, RemainingBytes=%d)",
			computedDownloaded, len(remainingTasks), remainingBytes)
		return nil // Graceful exit, not an error
	}

	// Handle cancel: context was cancelled but not via Pause() - just exit cleanly
	// The .surge file remains for cleanup by the TUI (which will delete it)
	if downloadCtx.Err() == context.Canceled {
		return nil
	}

	if downloadErr != nil {
		return downloadErr
	}

	// Final sync
	if err := outFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync file: %w", err)
	}

	// Close file before renaming
	outFile.Close()

	// Rename from .surge to final destination
	if err := os.Rename(workingPath, destPath); err != nil {
		return fmt.Errorf("failed to rename completed file: %w", err)
	}

	// Delete state file on successful completion
	_ = DeleteState(d.ID, d.URL, destPath)

	// Note: Download completion notifications are handled by the TUI via DownloadCompleteMsg

	return nil
}

// worker downloads tasks from the queue
func (d *ConcurrentDownloader) worker(ctx context.Context, id int, rawurl string, file *os.File, queue *TaskQueue, totalSize int64, startTime time.Time, verbose bool, client *http.Client) error {
	// Get pooled buffer
	bufPtr := bufPool.Get().(*[]byte)
	defer bufPool.Put(bufPtr)
	buf := *bufPtr

	utils.Debug("Worker %d started", id)
	defer utils.Debug("Worker %d finished", id)

	for {
		// Get next task
		task, ok := queue.Pop()

		if !ok {
			return nil // Queue closed, no more work
		}

		// Update active workers
		if d.State != nil {
			d.State.ActiveWorkers.Add(1)
		}

		var lastErr error
		maxRetries := d.Runtime.GetMaxTaskRetries()
		for attempt := 0; attempt < maxRetries; attempt++ {
			if attempt > 0 {
				time.Sleep(time.Duration(1<<attempt) * retryBaseDelay) //Exponential backoff incase of failure
			}

			// Register active task with per-task cancellable context
			taskCtx, taskCancel := context.WithCancel(ctx)
			now := time.Now()
			activeTask := &ActiveTask{
				Task:          task,
				CurrentOffset: task.Offset,
				StopAt:        task.Offset + task.Length,
				LastActivity:  now.UnixNano(),
				StartTime:     now,
				Cancel:        taskCancel,
				WindowStart:   now, // Initialize sliding window
			}
			d.activeMu.Lock()
			d.activeTasks[id] = activeTask
			d.activeMu.Unlock()

			taskStart := time.Now()
			lastErr = d.downloadTask(taskCtx, rawurl, file, activeTask, buf, verbose, client)

			// CRITICAL: Capture external cancellation state BEFORE calling taskCancel()
			// If we call taskCancel() first, taskCtx.Err() will always be non-nil
			wasExternallyCancelled := taskCtx.Err() != nil

			taskCancel() // Clean up context resources
			utils.Debug("Worker %d: Task offset=%d length=%d took %v", id, task.Offset, task.Length, time.Since(taskStart))

			// Check for PARENT context cancellation (pause/shutdown)
			// This preserves active task info for pause handler to collect
			if ctx.Err() != nil {
				// DON'T delete from activeTasks - pause handler needs it
				if d.State != nil {
					d.State.ActiveWorkers.Add(-1)
				}
				return ctx.Err()
			}

			// Check if TASK context was cancelled by Health Monitor (not by us calling taskCancel)
			// but parent context is still fine
			if wasExternallyCancelled && lastErr != nil {
				// Health monitor cancelled this task - re-queue REMAINING work only
				if remaining := activeTask.RemainingTask(); remaining != nil {
					// Clamp to original task end (don't go past original boundary)
					originalEnd := task.Offset + task.Length
					if remaining.Offset+remaining.Length > originalEnd {
						remaining.Length = originalEnd - remaining.Offset
					}
					if remaining.Length > 0 {
						queue.Push(*remaining)
						utils.Debug("Worker %d: health-cancelled task requeued (remaining: %d bytes from offset %d)",
							id, remaining.Length, remaining.Offset)
					}
				}
				// Delete from active tasks and move to next task (don't retry from scratch)
				d.activeMu.Lock()
				delete(d.activeTasks, id)
				d.activeMu.Unlock()
				// Clear lastErr so the fallthrough logic doesn't re-queue the original task
				lastErr = nil
				break // Exit retry loop, get next task
			}

			// Only delete from activeTasks on normal completion (not cancelled)
			d.activeMu.Lock()
			delete(d.activeTasks, id)
			d.activeMu.Unlock()

			if lastErr == nil {
				// Check if we stopped early due to stealing
				stopAt := atomic.LoadInt64(&activeTask.StopAt)
				current := atomic.LoadInt64(&activeTask.CurrentOffset)
				if current < task.Offset+task.Length && current >= stopAt {
					// We were stopped early this is expected success for the partial work
					// The stolen part is already in the queue
				}
				break
			}

			// Resume-on-retry: update task to reflect remaining work
			// This prevents double-counting bytes on retry
			current := atomic.LoadInt64(&activeTask.CurrentOffset)
			if current > task.Offset {
				task = Task{Offset: current, Length: task.Offset + task.Length - current}
			}
		}

		// Update active workers
		if d.State != nil {
			d.State.ActiveWorkers.Add(-1)
		}

		if lastErr != nil {
			// Log failed task but continue with next task
			// If we modified StopAt we should probably reset it or push the remaining part?
			// TODO: Could optimize by pushing only remaining part if we track that.
			queue.Push(task)
			utils.Debug("task at offset %d failed after %d retries: %v", task.Offset, maxRetries, lastErr)
		}
	}
}

// downloadTask downloads a single byte range and writes to file at offset
func (d *ConcurrentDownloader) downloadTask(ctx context.Context, rawurl string, file *os.File, activeTask *ActiveTask, buf []byte, verbose bool, client *http.Client) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return err
	}

	task := activeTask.Task

	req.Header.Set("User-Agent", d.Runtime.GetUserAgent())
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", task.Offset, task.Offset+task.Length-1))

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Handle rate limiting explicitly
	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("rate limited (429)")
	}

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	// Read and write at offset
	offset := task.Offset
	for {
		// Check if we should stop
		stopAt := atomic.LoadInt64(&activeTask.StopAt)
		if offset >= stopAt {
			// Stealing happened, stop here
			return nil
		}

		// Calculate how much to read to fill buffer or hit stopAt/EOF
		// We want to fill buf as much as possible to minimize WriteAt calls

		// Limit by remaining length to stopAt
		remaining := stopAt - offset
		if remaining <= 0 {
			return nil
		}

		readSize := int64(len(buf))
		if readSize > remaining {
			readSize = remaining
		}

		readSoFar := 0
		var readErr error

		for readSoFar < int(readSize) {
			n, err := resp.Body.Read(buf[readSoFar:readSize])
			if n > 0 {
				readSoFar += n
			}
			if err != nil {
				readErr = err
				break
			}
			if n == 0 {
				readErr = io.ErrUnexpectedEOF
				break
			}
		}

		if readSoFar > 0 {

			// check stopAt again before writing
			// truncate readSoFar
			currentStopAt := atomic.LoadInt64(&activeTask.StopAt)
			if offset+int64(readSoFar) > currentStopAt {
				readSoFar = int(currentStopAt - offset)
				if readSoFar <= 0 {
					return nil // stolen completely
				}
			}

			_, writeErr := file.WriteAt(buf[:readSoFar], offset)
			if writeErr != nil {
				return fmt.Errorf("write error: %w", writeErr)
			}

			now := time.Now()
			oldOffset := offset
			offset += int64(readSoFar)
			atomic.StoreInt64(&activeTask.CurrentOffset, offset)
			atomic.AddInt64(&activeTask.WindowBytes, int64(readSoFar))
			atomic.StoreInt64(&activeTask.LastActivity, now.UnixNano())

			// Update EMA speed using sliding window (2 second window)
			windowElapsed := now.Sub(activeTask.WindowStart).Seconds()
			if windowElapsed >= 2.0 {
				windowBytes := atomic.SwapInt64(&activeTask.WindowBytes, 0)
				recentSpeed := float64(windowBytes) / windowElapsed

				activeTask.SpeedMu.Lock()
				alpha := d.Runtime.GetSpeedEmaAlpha()
				if activeTask.Speed == 0 {
					activeTask.Speed = recentSpeed
				} else {
					activeTask.Speed = (1-alpha)*activeTask.Speed + alpha*recentSpeed
				}
				activeTask.SpeedMu.Unlock()

				activeTask.WindowStart = now // Reset window
			}

			// Update progress via shared state, clamping to StopAt boundary
			// to avoid double-counting bytes when work is stolen
			if d.State != nil {
				currentStopAt := atomic.LoadInt64(&activeTask.StopAt)
				effectiveEnd := offset
				if effectiveEnd > currentStopAt {
					effectiveEnd = currentStopAt
				}
				contributed := effectiveEnd - oldOffset
				if contributed > 0 {
					d.State.Downloaded.Add(contributed)
				}
			}
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read error: %w", readErr)
		}
	}

	return nil
}

// StealWork tries to split an active task from a busy worker
// It greedily targets the worker with the MOST remaining work.
func (d *ConcurrentDownloader) StealWork(queue *TaskQueue) bool {
	d.activeMu.Lock()
	defer d.activeMu.Unlock()

	var bestID int = -1
	var maxRemaining int64 = 0
	var bestActive *ActiveTask

	// Find the worker with the MOST remaining work
	for id, active := range d.activeTasks {
		remaining := active.RemainingBytes()
		if remaining > MinChunk && remaining > maxRemaining {
			maxRemaining = remaining
			bestID = id
			bestActive = active
		}
	}

	if bestID == -1 {
		return false
	}

	// Found the best candidate, now try to steal
	remaining := maxRemaining
	active := bestActive

	// Split in half, aligned to AlignSize
	splitSize := alignedSplitSize(remaining)
	if splitSize == 0 {
		return false
	}

	current := atomic.LoadInt64(&active.CurrentOffset)
	newStopAt := current + splitSize

	// Update the active task stop point
	atomic.StoreInt64(&active.StopAt, newStopAt)

	finalCurrent := atomic.LoadInt64(&active.CurrentOffset)

	// The actual start of the stolen chunk must be after where the worker effectively stops.
	stolenStart := newStopAt
	if finalCurrent > newStopAt {
		stolenStart = finalCurrent
	}

	// Double check: ensure we didn't race and lose the chunk
	currentStopAt := atomic.LoadInt64(&active.StopAt)
	if stolenStart >= currentStopAt && currentStopAt != newStopAt {
	}

	originalEnd := current + remaining

	if stolenStart >= originalEnd {
		return false
	}

	stolenTask := Task{
		Offset: stolenStart,
		Length: originalEnd - stolenStart,
	}

	queue.Push(stolenTask)
	utils.Debug("Balancer: stole %s from worker %d (new range: %d-%d)",
		utils.ConvertBytesToHumanReadable(stolenTask.Length), bestID, stolenTask.Offset, stolenTask.Offset+stolenTask.Length)

	return true
}

// checkWorkerHealth detects slow workers and cancels them
func (d *ConcurrentDownloader) checkWorkerHealth() {
	d.activeMu.Lock()
	defer d.activeMu.Unlock()

	if len(d.activeTasks) == 0 {
		return
	}

	now := time.Now()

	// First pass: calculate mean speed
	var totalSpeed float64
	var speedCount int
	for _, active := range d.activeTasks {
		if speed := active.GetSpeed(); speed > 0 {
			totalSpeed += speed
			speedCount++
		}
	}

	var meanSpeed float64
	if speedCount > 0 {
		meanSpeed = totalSpeed / float64(speedCount)
	}

	// Second pass: check for slow workers
	for workerID, active := range d.activeTasks {

		// timeSinceActivity := now.Sub(lastTime)
		taskDuration := now.Sub(active.StartTime)

		// Skip workers that are still in their grace period
		gracePeriod := d.Runtime.GetSlowWorkerGracePeriod()
		if taskDuration < gracePeriod {
			continue
		}

		// It is an invasive function which was causing workers to be killed too vigorously.
		// Maybe down the line we will add a timeout to kill workers after a worker is killed.
		// The current implementation achieves better speed without this

		// // Check for stalled worker (no activity for stallTimeout)
		// stallTimeout := d.Runtime.GetStallTimeout()
		// lastActivity := time.Unix(0, active.LastActivity)
		// if now.Sub(lastActivity) > stallTimeout {
		// 	utils.Debug("Health: Worker %d stalled (no activity for %v), cancelling", workerID, now.Sub(lastActivity))
		// 	if active.Cancel != nil {
		// 		active.Cancel()
		// 	}
		// 	continue
		// }

		// Check for slow worker
		// Only cancel if: below threshold
		if meanSpeed > 0 {
			workerSpeed := active.GetSpeed()
			threshold := d.Runtime.GetSlowWorkerThreshold()
			isBelowThreshold := workerSpeed > 0 && workerSpeed < threshold*meanSpeed

			if isBelowThreshold {
				utils.Debug("Health: Worker %d slow (%.2f KB/s vs mean %.2f KB/s, min %.2f KB/s), cancelling",
					workerID, workerSpeed/1024, meanSpeed/1024, float64(minAbsoluteSpeed)/1024)
				if active.Cancel != nil {
					active.Cancel()
				}
			}
		}
	}
}

package concurrent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/SurgeDM/Surge/internal/transport"
	"github.com/SurgeDM/Surge/internal/types"
	"github.com/SurgeDM/Surge/internal/utils"
)

// writeAtFn is injectable so tests can simulate ENOSPC without a full disk.
var writeAtFn = func(f *os.File, b []byte, off int64) (int, error) {
	return f.WriteAt(b, off)
}

var errSoftForbidden = errors.New("unexpected status: 403")

const soft403RetryDelay = 500 * time.Millisecond

// worker downloads tasks from the queue
func (d *ConcurrentDownloader) worker(ctx context.Context, id int, mirrors []string, file *os.File, queue *TaskQueue, totalSize int64, client *http.Client) error {
	bufPtr := d.bufPool.Get().(*[]byte)
	defer d.bufPool.Put(bufPtr)
	buf := *bufPtr

	utils.Debug("Worker %d started", id)
	defer utils.Debug("Worker %d finished", id)

	currentMirrorIdx := id % len(mirrors)

	mirrorHosts := make([]string, len(mirrors))
	for i, m := range mirrors {
		mirrorHosts[i] = transport.MirrorHost(m)
	}

	for {
		if d.concurrencyGate != nil && !d.concurrencyGate.acquire(ctx) {
			return ctx.Err()
		}
		task, ok := queue.Pop()
		if !ok {
			if d.concurrencyGate != nil {
				d.concurrencyGate.release()
			}
			return nil
		}

		if d.State != nil && d.State.Bytes.VerifiedProgress.Load() >= totalSize {
			if d.concurrencyGate != nil {
				d.concurrencyGate.release()
			}
			return nil
		}

		if d.State != nil {
			d.State.ActiveWorkers.Add(1)
		}

		now := time.Now()
		activeTask := &ActiveTask{
			Task:        task,
			StartTime:   now,
			WindowStart: now,
		}
		activeTask.CurrentOffset.Store(task.Offset)
		activeTask.StopAt.Store(task.Offset + task.Length)
		activeTask.LastActivity.Store(now.UnixNano())

		d.activeMu.Lock()
		if d.State != nil && d.State.Bytes.VerifiedProgress.Load() >= totalSize {
			d.activeMu.Unlock()
			d.State.ActiveWorkers.Add(-1)
			if d.concurrencyGate != nil {
				d.concurrencyGate.release()
			}
			return nil
		}
		d.activeTasks[id] = activeTask
		d.activeMu.Unlock()

		var lastErr error
		var throttledRequeue *types.Task
		maxRetries := d.Runtime.GetMaxTaskRetries()
		genericAttempt := 0

		for {
			idx, wait := d.hostLimiter.PickMirror(mirrorHosts, currentMirrorIdx, time.Now())
			currentMirrorIdx = idx
			if wait > 0 {
				activeTask.WaitingOnLimiter.Store(true)
				if !interruptibleSleep(ctx, wait) {
					activeTask.WaitingOnLimiter.Store(false)
					if d.State != nil {
						d.State.ActiveWorkers.Add(-1)
					}
					if d.concurrencyGate != nil {
						d.concurrencyGate.release()
					}
					return ctx.Err()
				}
				activeTask.WaitingOnLimiter.Store(false)
			}
			currentURL := mirrors[currentMirrorIdx]

			taskCtx, taskCancel := context.WithCancel(ctx)
			now := time.Now()

			d.activeMu.Lock()
			activeTask.Cancel = taskCancel
			activeTask.StartTime = now
			d.activeMu.Unlock()

			activeTask.WindowStart = now
			activeTask.WindowBytes.Store(0)
			activeTask.LastActivity.Store(now.UnixNano())

			if d.State != nil {
				utils.Debug("Worker %d: Setting range %d-%d to Downloading", id, task.Offset, task.Offset+task.Length)
				d.State.UpdateChunkStatus(task.Offset, task.Length, types.ChunkDownloading)
			} else {
				utils.Debug("Worker %d: d.State is nil, cannot update chunk status", id)
			}

			taskStart := time.Now()
			lastErr = d.downloadTask(taskCtx, currentURL, file, activeTask, buf, client, totalSize)

			wasExternallyCancelled := taskCtx.Err() != nil

			taskCancel()
			utils.Debug("Worker %d: Task offset=%d length=%d took %v", id, task.Offset, task.Length, time.Since(taskStart))

			// Disk full: fail immediately — no in-place retry, no mirror rotation,
			// no residual requeue. Stricter than permanent HTTP.
			// Stash RemainingTask off-queue so error-path saveStateSnapshot can
			// still persist the unfinished range after peers are cancelled.
			if types.IsInsufficientDiskSpace(lastErr) {
				var stash *types.Task
				if remaining := d.detachRemainingTask(id, activeTask); remaining != nil {
					originalEnd := task.Offset + task.Length
					if remaining.Offset+remaining.Length > originalEnd {
						remaining.Length = originalEnd - remaining.Offset
					}
					if remaining.Length > 0 {
						stash = remaining
					}
				}
				if stash != nil {
					d.activeMu.Lock()
					d.abandonedRemaining = append(d.abandonedRemaining, *stash)
					d.activeMu.Unlock()
				}
				if d.State != nil {
					d.State.ActiveWorkers.Add(-1)
				}
				if d.concurrencyGate != nil {
					d.concurrencyGate.release()
				}
				return lastErr
			}

			if ctx.Err() != nil {
				if d.State != nil {
					d.State.ActiveWorkers.Add(-1)
				}
				if d.concurrencyGate != nil {
					d.concurrencyGate.release()
				}
				return ctx.Err()
			}

			if wasExternallyCancelled && lastErr != nil {
				if d.completing.Load() {
					lastErr = nil
					break
				}
				currentMirrorIdx = (currentMirrorIdx + 1) % len(mirrors)
				utils.Debug("Worker %d: Health check cancelled task, rotating from mirror %s to %s", id, mirrors[(currentMirrorIdx+len(mirrors)-1)%len(mirrors)], mirrors[currentMirrorIdx])

				if remaining := d.detachRemainingTask(id, activeTask); remaining != nil {
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
				lastErr = nil
				break
			}

			if lastErr == nil {
				d.hostLimiter.RecordSuccess(mirrorHosts[currentMirrorIdx])
				stopAt := activeTask.StopAt.Load()
				current := activeTask.CurrentOffset.Load()
				if current < task.Offset+task.Length && current >= stopAt {
					utils.Debug("Worker stopped early due to stealing")
				}
				break
			}

			var rlErr *rateLimitError
			if errors.As(lastErr, &rlErr) {
				host := mirrorHosts[currentMirrorIdx]
				now := time.Now()
				until := d.hostLimiter.Penalize(host, rlErr.retryAfter, rlErr.explicit, now)
				if d.concurrencyGate != nil {
					oldCap, newCap, _ := d.concurrencyGate.throttle(now, until)
					if newCap < oldCap {
						utils.Debug("Adaptive concurrency: reduced cap from %d to %d after throttle", oldCap, newCap)
					}
				}
				if d.State == nil || !d.State.RateLimited.Swap(true) {
					wait := time.Until(until).Round(time.Second)
					message := fmt.Sprintf("Rate limited by %s; cooling down for %s", host, wait)
					utils.Debug("%s", message)
					if d.ProgressChan != nil {
						select {
						case d.ProgressChan <- types.DownloadEvent{Type: types.EventSystem, DownloadID: d.ID, Message: message}:
						default:
						}
					}
				}
				d.ReportMirrorError(currentURL)
				currentMirrorIdx = (currentMirrorIdx + 1) % len(mirrors)
				if remaining := d.detachRemainingTask(id, activeTask); remaining != nil && remaining.Length > 0 {
					throttledRequeue = remaining
				}
				lastErr = nil
				break
			}

			// Permanent HTTP (404/401/410/416): stop without burning
			// GetMaxTaskRetries or the single-mirror backoff. 403 stays
			// errSoftForbidden so confirmation-then-escalate still runs.
			if types.IsPermanentHTTPError(lastErr) {
				break
			}

			genericAttempt++
			if genericAttempt >= maxRetries {
				break
			}
			d.ReportMirrorError(mirrors[currentMirrorIdx])
			currentMirrorIdx = (currentMirrorIdx + 1) % len(mirrors)
			if len(mirrors) == 1 {
				activeTask.WaitingOnLimiter.Store(true)
				interruptibleSleep(ctx, time.Duration(1<<genericAttempt)*types.RetryBaseDelay)
				activeTask.WaitingOnLimiter.Store(false)
			}
			resumeOnRetryOffset(&task, activeTask)
		}

		if d.State != nil {
			d.State.ActiveWorkers.Add(-1)
		}

		d.activeMu.Lock()
		delete(d.activeTasks, id)
		d.activeMu.Unlock()
		if d.concurrencyGate != nil {
			d.concurrencyGate.release()
		}

		if d.completing.Load() {
			return nil
		}

		if throttledRequeue != nil {
			queue.Push(*throttledRequeue)
			utils.Debug("Worker %d: throttled task requeued (remaining: %d bytes from offset %d)",
				id, throttledRequeue.Length, throttledRequeue.Offset)
			continue
		}

		if lastErr != nil {
			utils.Debug("Worker %d: task at offset %d failed after %d retries: %v", id, task.Offset, maxRetries, lastErr)

			remain := activeTask.RemainingTask()

			if errors.Is(lastErr, errSoftForbidden) {
				if !d.shouldEscalate403(time.Now()) {
					if !interruptibleSleep(ctx, soft403RetryDelay) {
						if remain != nil {
							queue.Push(*remain)
						}
						return ctx.Err()
					}
					if remain != nil {
						queue.Push(*remain)
					}
					continue
				}
				lastErr = fmt.Errorf("%v: %w", lastErr, types.ErrPermanentHTTP)
			}
			if remain != nil {
				queue.Push(*remain)
			}

			return lastErr
		}
	}
}

// detachRemainingTask removes an active task from the steal set and snapshots
// its remaining range under the documented activeMu -> RangeMu lock order.
func (d *ConcurrentDownloader) detachRemainingTask(id int, active *ActiveTask) *types.Task {
	d.activeMu.Lock()
	defer d.activeMu.Unlock()
	delete(d.activeTasks, id)
	return active.RemainingTask()
}

// downloadTask downloads a single byte range and writes to file at offset
func (d *ConcurrentDownloader) downloadTask(ctx context.Context, rawurl string, file *os.File, activeTask *ActiveTask, buf []byte, client *http.Client, totalSize int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return err
	}

	task := activeTask.Task

	// Apply custom headers first (from browser extension: cookies, auth, referer, etc.)
	for key, val := range d.Headers {
		// Skip Range header - we set it ourselves for parallel downloads
		if key != "Range" {
			req.Header.Set(key, val)
		}
	}

	// Set User-Agent from config only if not provided in custom headers
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", d.Runtime.GetUserAgent())
	}
	// Range header is always set for partial downloads (overrides any browser Range header)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", task.Offset, task.Offset+task.Length-1))

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			utils.Debug("Error closing response body: %v", err)
		}
	}()

	// Handle rate limiting explicitly
	if resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusServiceUnavailable && resp.Header.Get("Retry-After") != "") {
		ra, ok := transport.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		return &rateLimitError{retryAfter: ra, explicit: ok}
	}

	// Validate status code
	if resp.StatusCode == http.StatusOK {
		// Valid only if we requested the full file
		// If we wanted a partial range but got the whole file (200), that's an error because we can't handle the full stream at a non-zero offset
		if task.Offset != 0 || task.Length != totalSize {
			return fmt.Errorf("server indicated success (200) but ignored range request (expected 206)")
		}
	} else if resp.StatusCode != http.StatusPartialContent {
		if resp.StatusCode == http.StatusForbidden {
			return errSoftForbidden
		}
		if types.IsPermanentHTTPStatus(resp.StatusCode) {
			return fmt.Errorf("unexpected status: %d: %w", resp.StatusCode, types.ErrPermanentHTTP)
		}
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	// Batching State
	var pendingBytes int64
	var pendingStart int64 = -1
	lastUpdate := time.Now()
	batchSizeThreshold := int64(types.WorkerBatchSize)
	batchTimeThreshold := types.WorkerBatchInterval

	// Helper to flush pending updates to global state
	flushUpdates := func() {
		if pendingBytes > 0 && d.State != nil {
			// Update Chunk Map (Global Lock)
			d.State.UpdateChunkStatus(pendingStart, pendingBytes, types.ChunkCompleted)

			// Update Downloaded Counter (Atomic)
			d.State.Bytes.Downloaded.Add(pendingBytes)

			pendingBytes = 0
			pendingStart = -1
			lastUpdate = time.Now()
		}
	}
	// Ensure we flush whatever we have on exit
	defer flushUpdates()

	// Read and write at offset
	offset := task.Offset
	for {
		// Check if we should stop
		stopAt := activeTask.StopAt.Load()
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
				// CONTINUOUS HEALTH KEEPALIVE:
				// Update LastActivity directly off the TCP socket instead of waiting for the buffer
				// to completely fill and hit disk. This prevents the Health Monitor from killing
				// workers on slightly slower networks during the 500KB buffer acquisition.
				activeTask.LastActivity.Store(time.Now().UnixNano())
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
			if d.Limiter != nil {
				// Reset stall clock before the wait so the health monitor measures
				// time from when throttling begins, not from the last network read.
				activeTask.LastActivity.Store(time.Now().UnixNano())
				activeTask.WaitingOnLimiter.Store(true)
				err := d.Limiter.WaitN(ctx, int64(readSoFar))
				activeTask.WaitingOnLimiter.Store(false)
				if err != nil {
					return err
				}

				// Refresh again after the wait to keep the stall clock current.
				activeTask.LastActivity.Store(time.Now().UnixNano())
			}

			activeTask.RangeMu.Lock()
			// Recheck the boundary while excluding StealWork. Without this lock,
			// stealing between the check and WriteAt could overlap the new task.
			currentStopAt := activeTask.StopAt.Load()
			if offset+int64(readSoFar) > currentStopAt {
				readSoFar = int(currentStopAt - offset)
				if readSoFar <= 0 {
					activeTask.RangeMu.Unlock()
					return nil // stolen completely
				}
			}

			_, writeErr := writeAtFn(file, buf[:readSoFar], offset)
			if writeErr != nil {
				activeTask.RangeMu.Unlock()
				return fmt.Errorf("write error: %w", writeErr)
			}

			now := time.Now()
			offset += int64(readSoFar)
			newlyWritten := int64(readSoFar)

			offset, newlyWritten = clampWriteToStopAt(offset, newlyWritten, activeTask.StopAt.Load())
			activeTask.CurrentOffset.Store(offset)
			activeTask.RangeMu.Unlock()

			offset, newlyWritten = clampWriteToStopAt(offset, newlyWritten, activeTask.StopAt.Load())
			activeTask.WindowBytes.Add(newlyWritten)
			activeTask.LastActivity.Store(now.UnixNano())

			// Calculate effective contribution
			if newlyWritten > 0 {
				if pendingStart == -1 {
					pendingStart = offset - newlyWritten
				}
				pendingBytes += newlyWritten
			}

			// Check thresholds
			if pendingBytes >= batchSizeThreshold || now.Sub(lastUpdate) >= batchTimeThreshold {
				flushUpdates()
			}

			// Update EMA speed using sliding window (2 second window)
			// This relies on WindowBytes which is updated atomically above, so independent of batching
			windowElapsed := now.Sub(activeTask.WindowStart).Seconds()
			if windowElapsed >= 2.0 {
				windowBytes := activeTask.WindowBytes.Swap(0)
				recentSpeed := float64(windowBytes) / windowElapsed

				activeTask.SpeedMu.Lock()
				alpha := d.Runtime.GetSpeedEmaAlpha()
				if alpha <= 0 || activeTask.Speed == 0 {
					// Alpha 0 disables smoothing and uses the latest measured speed directly.
					activeTask.Speed = recentSpeed
				} else {
					activeTask.Speed = (1-alpha)*activeTask.Speed + alpha*recentSpeed
				}
				activeTask.SpeedMu.Unlock()

				activeTask.WindowStart = now // Reset window
			}
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read error: %w", readErr)
		}
	}

	// Clean io.EOF with offset still short of StopAt is a truncated body,
	// not success. UnexpectedEOF stays on the read-error path above.
	stopAt := activeTask.StopAt.Load()
	if offset < stopAt {
		return fmt.Errorf("early EOF: read up to %d, expected %d", offset, stopAt)
	}

	return nil
}

// StealWork tries to split an active task from a busy worker
// It greedily targets the worker with the MOST remaining work.
func (d *ConcurrentDownloader) StealWork(queue *TaskQueue) bool {
	d.activeMu.Lock()
	defer d.activeMu.Unlock()

	splitFloor := d.Runtime.GetMinChunkSize()
	if d.Runtime.IsAdaptiveConcurrencyEnabled() &&
		(d.concurrencyGate == nil || !d.concurrencyGate.sawThrottle()) {
		splitFloor = types.AlignSize
	}

	bestID := -1
	var maxRemaining int64 = 0
	var bestActive *ActiveTask

	// Find the worker with the MOST remaining work
	for id, active := range d.activeTasks {
		remaining := active.RemainingBytes()
		if remaining > splitFloor && remaining > maxRemaining {
			maxRemaining = remaining
			bestID = id
			bestActive = active
		}
	}

	if bestID == -1 {
		return false
	}

	// Found the best candidate, now try to steal
	active := bestActive
	active.RangeMu.Lock()
	defer active.RangeMu.Unlock()
	current := active.CurrentOffset.Load()
	originalEnd := active.StopAt.Load()
	remaining := originalEnd - current
	if remaining <= 0 {
		return false
	}

	// Split in half, aligned to AlignSize
	splitSize := alignedSplitSize(remaining, splitFloor)
	if splitSize == 0 {
		return false
	}

	newStopAt := current + splitSize

	// Update the active task stop point
	active.StopAt.Store(newStopAt)

	stolenTask := types.Task{
		Offset: newStopAt,
		Length: originalEnd - newStopAt,
	}

	queue.Push(stolenTask)
	utils.Debug("Balancer: stole %s from worker %d (new range: %d-%d)",
		utils.FormatBytes(stolenTask.Length), bestID, stolenTask.Offset, stolenTask.Offset+stolenTask.Length)

	return true
}

func clampWriteToStopAt(offset, newlyWritten, stopAt int64) (int64, int64) {
	if offset > stopAt {
		excess := offset - stopAt
		offset = stopAt
		if newlyWritten > excess {
			newlyWritten -= excess
		} else {
			newlyWritten = 0
		}
	}
	return offset, newlyWritten
}

func resumeOnRetryOffset(task *types.Task, activeTask *ActiveTask) {
	current := activeTask.CurrentOffset.Load()
	origEnd := task.Offset + task.Length
	stopAt := activeTask.StopAt.Load()
	effectiveEnd := stopAt
	if origEnd < effectiveEnd {
		effectiveEnd = origEnd
	}
	length := effectiveEnd - current
	if length < 0 {
		length = 0
	}
	task.Offset = current
	task.Length = length
	activeTask.Task = *task
}

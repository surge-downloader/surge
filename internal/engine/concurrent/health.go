package concurrent

import (
	"time"

	"github.com/surge-downloader/surge/internal/engine/types"
	"github.com/surge-downloader/surge/internal/utils"
)

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

		// Check for slow worker
		// Only cancel if: below threshold
		if meanSpeed > 0 {
			workerSpeed := active.GetSpeed()
			threshold := d.Runtime.GetSlowWorkerThreshold()
			isBelowThreshold := workerSpeed > 0 && workerSpeed < threshold*meanSpeed

			if isBelowThreshold {
				utils.Debug("Health: Worker %d slow (%.2f KB/s vs mean %.2f KB/s, min %.2f KB/s), cancelling",
					workerID, workerSpeed/1024, meanSpeed/1024, float64(types.KB*100)/1024)
				if active.Cancel != nil {
					active.Cancel()
				}
			}
		}
	}
}

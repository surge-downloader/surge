package single

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/surge-downloader/surge/internal/engine/types"
	"github.com/surge-downloader/surge/internal/utils"
)

// SingleDownloader handles single-threaded downloads for servers that don't support range requests.
// NOTE: Pause/resume is NOT supported because this downloader is only used when
// the server doesn't support Range headers. If interrupted, the download must restart.
type SingleDownloader struct {
	Client       *http.Client
	ProgressChan chan<- any           // Channel for events (start/complete/error)
	ID           string               // Download ID
	State        *types.ProgressState // Shared state for TUI polling
	Runtime      *types.RuntimeConfig
}

// NewSingleDownloader creates a new single-threaded downloader with all required parameters
func NewSingleDownloader(id string, progressCh chan<- any, state *types.ProgressState, runtime *types.RuntimeConfig) *SingleDownloader {
	return &SingleDownloader{
		Client:       &http.Client{Timeout: 0},
		ProgressChan: progressCh,
		ID:           id,
		State:        state,
		Runtime:      runtime,
	}
}

// Download downloads a file using a single connection.
// This is used for servers that don't support Range requests.
// If interrupted, the download cannot be resumed and must restart from the beginning.
func (d *SingleDownloader) Download(ctx context.Context, rawurl, destPath string, fileSize int64, filename string, verbose bool) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return err
	}

	req.Header.Set("User-Agent", d.Runtime.GetUserAgent())

	resp, err := d.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Use .surge extension for incomplete file
	workingPath := destPath + types.IncompleteSuffix
	outFile, err := os.Create(workingPath)
	if err != nil {
		return err
	}

	// Track whether we completed successfully for cleanup
	success := false
	defer func() {
		outFile.Close()
		if !success {
			os.Remove(workingPath)
		}
	}()

	start := time.Now()

	// Copy response body to file with context cancellation support
	var written int64
	buf := make([]byte, d.Runtime.GetWorkerBufferSize())

	for {
		// Check for context cancellation (allows clean shutdown)
		select {
		case <-ctx.Done():
			// Can't resume - server doesn't support Range requests
			return ctx.Err()
		default:
		}

		nr, readErr := resp.Body.Read(buf)
		if nr > 0 {
			nw, writeErr := outFile.Write(buf[0:nr])
			if nw > 0 {
				written += int64(nw)
				if d.State != nil {
					d.State.Downloaded.Store(written)
				}
			}
			if writeErr != nil {
				return fmt.Errorf("write error: %w", writeErr)
			}
			if nr != nw {
				return io.ErrShortWrite
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break // Done reading
			}
			return fmt.Errorf("read error: %w", readErr)
		}
	}

	if err := outFile.Sync(); err != nil {
		return fmt.Errorf("sync error: %w", err)
	}
	if err := outFile.Close(); err != nil {
		return fmt.Errorf("close error: %w", err)
	}

	// Rename .surge file to final destination
	if err := os.Rename(workingPath, destPath); err != nil {
		// Fallback: copy if rename fails (cross-device)
		if copyErr := copyFile(workingPath, destPath); copyErr != nil {
			return fmt.Errorf("failed to finalize file: %w", copyErr)
		}
		os.Remove(workingPath)
	}

	success = true // Mark successful so defer doesn't clean up

	// Only print stats in verbose mode
	if verbose {
		elapsed := time.Since(start)
		speed := float64(written) / elapsed.Seconds()
		fmt.Fprintf(os.Stderr, "\nDownloaded %s in %s (%s/s)\n",
			destPath,
			elapsed.Round(time.Second),
			utils.ConvertBytesToHumanReadable(int64(speed)),
		)
	}

	return nil
}

// copyFile copies a file from src to dst (fallback when rename fails)
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

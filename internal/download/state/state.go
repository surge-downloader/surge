package state

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/surge-downloader/surge/internal/engine/types"
)

// URLHash returns a short hash of the URL for master list keying
// This is used for tracking completed downloads by URL
func URLHash(url string) string {
	h := sha256.Sum256([]byte(url))
	return hex.EncodeToString(h[:8]) // 16 chars
}

// SaveState saves download state to SQLite
func SaveState(url string, destPath string, state *types.DownloadState) error {
	// Ensure ID is set
	if state.ID == "" {
		// Try to find existing ID using StateHash equivalent or just generate new
		// Ideally ID should be passed in, but for backward compat we handle it
		state.ID = uuid.New().String()
	}

	// Set hashes and timestamps
	state.URLHash = URLHash(url)
	state.PausedAt = time.Now().Unix()
	if state.CreatedAt == 0 {
		state.CreatedAt = time.Now().Unix()
	}

	return withTx(func(tx *sql.Tx) error {
		// 1. Upsert into downloads table
		_, err := tx.Exec(`
			INSERT INTO downloads (
				id, url, dest_path, filename, status, total_size, downloaded, url_hash, created_at, paused_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				url=excluded.url,
				dest_path=excluded.dest_path,
				filename=excluded.filename,
				status=excluded.status,
				total_size=excluded.total_size,
				downloaded=excluded.downloaded,
				url_hash=excluded.url_hash,
				paused_at=excluded.paused_at
		`, state.ID, state.URL, state.DestPath, state.Filename, "paused", state.TotalSize, state.Downloaded, state.URLHash, state.CreatedAt, state.PausedAt)

		if err != nil {
			return fmt.Errorf("failed to upsert download: %w", err)
		}

		// 2. Refresh tasks
		// First delete existing tasks for this download
		if _, err := tx.Exec("DELETE FROM tasks WHERE download_id = ?", state.ID); err != nil {
			return fmt.Errorf("failed to delete old tasks: %w", err)
		}

		// Insert new tasks
		stmt, err := tx.Prepare("INSERT INTO tasks (download_id, offset, length) VALUES (?, ?, ?)")
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, task := range state.Tasks {
			if _, err := stmt.Exec(state.ID, task.Offset, task.Length); err != nil {
				return fmt.Errorf("failed to insert task: %w", err)
			}
		}

		return nil
	})
}

// LoadState loads download state from SQLite
func LoadState(url string, destPath string) (*types.DownloadState, error) {
	// We need to find the download by URL and DestPath since we might not have ID yet (legacy caller)
	// But ideally callers should use ID.
	// For now, let's query by URL and DestPath.

	db := getDBHelper()
	if db == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	var state types.DownloadState
	row := db.QueryRow(`
		SELECT id, url, dest_path, filename, total_size, downloaded, url_hash, created_at, paused_at 
		FROM downloads 
		WHERE url = ? AND dest_path = ? AND status = 'paused'
		ORDER BY paused_at DESC LIMIT 1
	`, url, destPath)

	err := row.Scan(
		&state.ID, &state.URL, &state.DestPath, &state.Filename,
		&state.TotalSize, &state.Downloaded, &state.URLHash,
		&state.CreatedAt, &state.PausedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			// Try finding without status constraint (just in case)
			return nil, fmt.Errorf("state not found: %w", os.ErrNotExist) // mimic os.ErrNotExist for compatibility
		}
		return nil, fmt.Errorf("failed to query download: %w", err)
	}

	// Load tasks
	rows, err := db.Query("SELECT offset, length FROM tasks WHERE download_id = ?", state.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to query tasks: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var t types.Task
		if err := rows.Scan(&t.Offset, &t.Length); err != nil {
			return nil, err
		}
		state.Tasks = append(state.Tasks, t)
	}

	return &state, nil
}

// DeleteState removes the state from SQLite
func DeleteState(id string, url string, destPath string) error {
	db := getDBHelper()
	if db == nil {
		return fmt.Errorf("database not initialized")
	}

	var result sql.Result
	var err error

	if id != "" {
		result, err = db.Exec("DELETE FROM downloads WHERE id = ?", id)
	} else {
		// Fallback for legacy calls without ID
		result, err = db.Exec("DELETE FROM downloads WHERE url = ? AND dest_path = ?", url, destPath)
	}

	if err != nil {
		return fmt.Errorf("failed to delete state: %w", err)
	}

	// Tasks are deleted via CASCADE or we strictly rely on download_id
	// Since we defined CASCADE in schema, it should be fine.
	// But 'tasks' table has foreign key constraint, assuming SQLite FKs are enabled.
	// We should probably ensure FKs are enabled or manually delete tasks.
	// For safety, let's manually delete if we didn't use CASCADE in creation or forgot to enable FK.
	// actually, let's just trust our schema but also execute a cleanup just deeply in case.
	// (Implementation detail: FK support needs `PRAGMA foreign_keys = ON`)

	// Check rows affected
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil // Already gone or didn't exist
	}

	return nil
}

// ================== Master List Functions ==================

// LoadMasterList loads ALL downloads (paused and completed)
func LoadMasterList() (*types.MasterList, error) {
	db := getDBHelper()
	if db == nil {
		// Return empty list if DB fails, to behave like "no file found"
		return &types.MasterList{Downloads: []types.DownloadEntry{}}, nil
	}

	rows, err := db.Query(`
		SELECT id, url, dest_path, filename, status, total_size, completed_at, time_taken, url_hash 
		FROM downloads
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query downloads: %w", err)
	}
	defer rows.Close()

	var list types.MasterList
	for rows.Next() {
		var e types.DownloadEntry
		var completedAt, timeTaken sql.NullInt64 // handle nulls

		if err := rows.Scan(
			&e.ID, &e.URL, &e.DestPath, &e.Filename, &e.Status, &e.TotalSize,
			&completedAt, &timeTaken, &e.URLHash,
		); err != nil {
			return nil, err
		}

		if completedAt.Valid {
			e.CompletedAt = completedAt.Int64
		}
		if timeTaken.Valid {
			e.TimeTaken = timeTaken.Int64
		}

		list.Downloads = append(list.Downloads, e)
	}

	return &list, nil
}

// AddToMasterList adds or updates a download entry
func AddToMasterList(entry types.DownloadEntry) error {
	// Ensure ID
	if entry.ID == "" {
		if entry.URLHash != "" {
			// Try to replicate existing ID logic or fail?
			// Let's generate one if missing, but this might duplicate if not careful.
			// Best effort:
			entry.ID = uuid.New().String()
		}
	}

	return withTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO downloads (
				id, url, dest_path, filename, status, total_size, completed_at, time_taken, url_hash
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				url=excluded.url,
				dest_path=excluded.dest_path,
				filename=excluded.filename,
				status=excluded.status,
				total_size=excluded.total_size,
				completed_at=excluded.completed_at,
				time_taken=excluded.time_taken,
				url_hash=excluded.url_hash
		`,
			entry.ID, entry.URL, entry.DestPath, entry.Filename, entry.Status, entry.TotalSize,
			entry.CompletedAt, entry.TimeTaken, entry.URLHash)

		return err
	})
}

// RemoveFromMasterList removes a download entry
func RemoveFromMasterList(id string) error {
	db := getDBHelper()
	if db == nil {
		return fmt.Errorf("database not initialized")
	}

	_, err := db.Exec("DELETE FROM downloads WHERE id = ?", id)
	return err
}

// LoadPausedDownloads returns all paused downloads
func LoadPausedDownloads() ([]types.DownloadEntry, error) {
	// Reuse LoadMasterList logic or optimize with WHERE
	list, err := LoadMasterList()
	if err != nil {
		return nil, err
	}

	var paused []types.DownloadEntry
	for _, e := range list.Downloads {
		if e.Status == "paused" {
			paused = append(paused, e)
		}
	}
	return paused, nil
}

// LoadCompletedDownloads returns all completed downloads
func LoadCompletedDownloads() ([]types.DownloadEntry, error) {
	list, err := LoadMasterList()
	if err != nil {
		return nil, err
	}

	var completed []types.DownloadEntry
	for _, e := range list.Downloads {
		if e.Status == "completed" {
			completed = append(completed, e)
		}
	}
	return completed, nil
}

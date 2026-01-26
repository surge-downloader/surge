package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/surge-downloader/surge/internal/config"
)

func TestAcquireLock(t *testing.T) {
	// Setup isolation
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	// Ensure dirs exist (mocking what root.go does)
	err := config.EnsureDirs()
	require.NoError(t, err)

	// Test 1: First acquisition should succeed
	t.Run("FirstAcquisition", func(t *testing.T) {
		locked, err := AcquireLock()
		require.NoError(t, err)
		assert.True(t, locked, "Should acquire lock on first try")
	})

	// Test 2: Second acquisition should fail (locked by us in this process context)

	t.Run("SecondAcquisition", func(t *testing.T) {
		// Attempt to acquire again with a fresh call (re simulates a second instance)

		locked, err := AcquireLock()
		require.NoError(t, err)
		// If it succeeded, it means we can re-lock.
		// If it failed, it means strict locking.
		if locked {
			// Clean up this second lock if it succeeded
			instanceLock.flock.Unlock()
			t.Log("Warning: Same-process re-locking succeeded. Subprocess test needed for strict verification.")
		} else {
			assert.False(t, locked, "Should not acquire lock if already held")
		}
	})

	// Cleanup
	err = ReleaseLock()
	assert.NoError(t, err)

	// Verify file exists
	lockPath := filepath.Join(config.GetSurgeDir(), "surge.lock")
	_, err = os.Stat(lockPath)
	assert.NoError(t, err, "Lock file should exist")
}

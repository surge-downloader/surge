package concurrent

import (
	"path/filepath"
	"testing"

	"github.com/SurgeDM/Surge/internal/progress"
	"github.com/SurgeDM/Surge/internal/types"
)

// buildCompletedBitmap encodes a 2-bit-per-chunk bitmap marking the first
// numCompleted chunks as ChunkCompleted (status=2).
func buildCompletedBitmap(totalSize, chunkSize int64, numCompleted int) []byte {
	numChunks := int((totalSize + chunkSize - 1) / chunkSize)
	if numChunks <= 0 {
		return nil
	}
	bytesNeeded := (numChunks + 3) / 4
	bm := make([]byte, bytesNeeded)
	for i := 0; i < numCompleted && i < numChunks; i++ {
		byteIndex := i / 4
		bitOffset := (i % 4) * 2
		bm[byteIndex] |= byte(types.ChunkCompleted) << bitOffset
	}
	return bm
}

func resumeSetup(t *testing.T, id string, fileSize, chunkSize int64, saved *types.DownloadRecord) *progress.DownloadProgress {
	t.Helper()
	tmpDir, cleanup := initTestState(t)
	t.Cleanup(cleanup)

	destPath := filepath.Join(tmpDir, id+".bin")
	state := progress.New(id, fileSize)
	d := &ConcurrentDownloader{ID: id, State: state}
	if _, err := d.setupTasks(destPath, fileSize, chunkSize, 4, nil, saved, true); err != nil {
		t.Fatalf("setupTasks failed: %v", err)
	}
	return state
}

func TestResume_BitmapTrust_InflatedDownloadedDoesNotOverrideVP(t *testing.T) {
	fileSize := int64(10000)
	chunkSize := int64(1000)
	saved := &types.DownloadRecord{
		Downloaded:      fileSize,
		ActualChunkSize: chunkSize,
		ChunkBitmap:     buildCompletedBitmap(fileSize, chunkSize, 5),
		Tasks:           []types.Task{{Offset: 5000, Length: 5000}},
	}

	state := resumeSetup(t, "inflate-multi", fileSize, chunkSize, saved)
	vp := state.Bytes.VerifiedProgress.Load()
	downloaded := state.Bytes.Downloaded.Load()

	if vp != 5000 {
		t.Errorf("VP = %d, want 5000 (bitmap truth, not inflated Downloaded)", vp)
	}
	if downloaded != fileSize {
		t.Errorf("Downloaded = %d, want %d (max(saved, VP) keeps inflated saved)", downloaded, fileSize)
	}
	if got := state.Session.GetSessionStartBytesForTest(); got != vp {
		t.Errorf("SessionStartBytes = %d, want VP %d", got, vp)
	}
}

func TestResume_BitmapTrust_SingleChunkVPZeroInflatedDownloaded(t *testing.T) {
	fileSize := int64(58000000)
	chunkSize := fileSize
	saved := &types.DownloadRecord{
		Downloaded:      fileSize,
		ActualChunkSize: chunkSize,
		ChunkBitmap:     buildCompletedBitmap(fileSize, chunkSize, 0),
		Tasks:           []types.Task{{Offset: 0, Length: fileSize}},
	}

	state := resumeSetup(t, "inflate-single", fileSize, chunkSize, saved)
	vp := state.Bytes.VerifiedProgress.Load()
	downloaded := state.Bytes.Downloaded.Load()

	if vp != 0 {
		t.Errorf("VP = %d, want 0 (no chunk Completed — do not fall back to saved Downloaded)", vp)
	}
	if downloaded != fileSize {
		t.Errorf("Downloaded = %d, want %d", downloaded, fileSize)
	}
	if got := state.Session.GetSessionStartBytesForTest(); got != vp {
		t.Errorf("SessionStartBytes = %d, want VP %d", got, vp)
	}
}

func TestResume_NoBitmap_LegacyVPEqualsDownloaded(t *testing.T) {
	fileSize := int64(10000)
	chunkSize := int64(1000)
	saved := &types.DownloadRecord{
		Downloaded: 7000,
		Tasks:      []types.Task{{Offset: 7000, Length: 3000}},
	}

	state := resumeSetup(t, "legacy-nobitmap", fileSize, chunkSize, saved)
	vp := state.Bytes.VerifiedProgress.Load()
	downloaded := state.Bytes.Downloaded.Load()

	if vp != 7000 {
		t.Errorf("VP = %d, want 7000 (legacy: VP = saved Downloaded)", vp)
	}
	if downloaded != 7000 {
		t.Errorf("Downloaded = %d, want 7000", downloaded)
	}
	if got := state.Session.GetSessionStartBytesForTest(); got != vp {
		t.Errorf("SessionStartBytes = %d, want VP %d", got, vp)
	}
}

func TestResume_BitmapTrust_AllZeroBitmapBenignRegression(t *testing.T) {
	fileSize := int64(10000)
	chunkSize := int64(1000)
	saved := &types.DownloadRecord{
		Downloaded:      9000,
		ActualChunkSize: chunkSize,
		ChunkBitmap:     buildCompletedBitmap(fileSize, chunkSize, 0),
		Tasks:           []types.Task{{Offset: 0, Length: fileSize}},
	}

	state := resumeSetup(t, "allzero-bitmap", fileSize, chunkSize, saved)
	vp := state.Bytes.VerifiedProgress.Load()
	downloaded := state.Bytes.Downloaded.Load()

	if vp != 0 {
		t.Errorf("VP = %d, want 0 (all-zero bitmap is Recalculate truth)", vp)
	}
	if downloaded != 9000 {
		t.Errorf("Downloaded = %d, want 9000 (max(saved, VP) keeps saved)", downloaded)
	}
	if got := state.Session.GetSessionStartBytesForTest(); got != vp {
		t.Errorf("SessionStartBytes = %d, want VP %d", got, vp)
	}
}

package processing

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyChecksum_SHA256(t *testing.T) {
	// Create a temp file with known content
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	content := []byte("hello surge")
	require.NoError(t, os.WriteFile(path, content, 0o644))

	// Compute expected hash
	h := sha256.Sum256(content)
	expected := hex.EncodeToString(h[:])

	result, err := VerifyChecksum(path, "sha256", expected)
	require.NoError(t, err)
	assert.True(t, result.Match)
	assert.Equal(t, expected, result.Actual)
}

func TestVerifyChecksum_MD5(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	content := []byte("hello surge")
	require.NoError(t, os.WriteFile(path, content, 0o644))

	h := md5.Sum(content)
	expected := hex.EncodeToString(h[:])

	result, err := VerifyChecksum(path, "md5", expected)
	require.NoError(t, err)
	assert.True(t, result.Match)
	assert.Equal(t, "md5", result.Algorithm)
	assert.Equal(t, expected, result.Actual)
}

func TestVerifyChecksum_SHA1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	content := []byte("hello surge")
	require.NoError(t, os.WriteFile(path, content, 0o644))

	h := sha1.Sum(content)
	expected := hex.EncodeToString(h[:])

	result, err := VerifyChecksum(path, "sha-1", expected)
	require.NoError(t, err)
	assert.True(t, result.Match)
	assert.Equal(t, "sha1", result.Algorithm)
	assert.Equal(t, expected, result.Actual)
}

func TestVerifyChecksum_Mismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	require.NoError(t, os.WriteFile(path, []byte("hello"), 0o644))

	result, err := VerifyChecksum(path, "sha256", "0000000000000000000000000000000000000000000000000000000000000000")
	require.NoError(t, err)
	assert.False(t, result.Match)
}

func TestVerifyChecksum_UnsupportedAlgorithm(t *testing.T) {
	_, err := VerifyChecksum("/tmp/test", "sha512", "abc")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
}

func TestVerifyChecksum_EmptyArgs(t *testing.T) {
	_, err := VerifyChecksum("", "sha256", "abc")
	assert.Error(t, err)
}

func mustParseDigestHeader(t *testing.T, header string) (string, string) {
	t.Helper()
	algo, hash, err := ParseDigestHeader(header)
	require.NoError(t, err)
	return algo, hash
}

func TestParseDigestHeader_SHA256Base64(t *testing.T) {
	// sha256 of empty string in base64
	algo, hash := mustParseDigestHeader(t, "sha-256=47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=")
	assert.Equal(t, "sha256", algo)
	assert.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", hash)
}

func TestParseDigestHeader_MD5Hex(t *testing.T) {
	algo, hash := mustParseDigestHeader(t, "md5=d41d8cd98f00b204e9800998ecf8427e")
	assert.Equal(t, "md5", algo)
	assert.Equal(t, "d41d8cd98f00b204e9800998ecf8427e", hash)
}

func TestParseDigestHeader_Invalid(t *testing.T) {
	algo, hash := mustParseDigestHeader(t, "invalid")
	assert.Empty(t, algo)
	assert.Empty(t, hash)
}

func TestParseDigestHeader_UnsupportedAlgo(t *testing.T) {
	algo, hash := mustParseDigestHeader(t, "sha-512=abc")
	assert.Empty(t, algo)
	assert.Empty(t, hash)
}

func TestParseDigestHeader_SHA256UnpaddedBase64(t *testing.T) {
	algo, hash := mustParseDigestHeader(t, "sha-256=47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU")
	assert.Equal(t, "sha256", algo)
	assert.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", hash)
}

func TestParseDigestHeader_SHA256WrongLengthHex(t *testing.T) {
	_, _, err := ParseDigestHeader("sha-256=d41d8cd98f00b204e9800998ecf8427e")
	require.Error(t, err)
}

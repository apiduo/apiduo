package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExportFilePathRejectsTraversal(t *testing.T) {
	t.Setenv(ExportFileDirEnv, t.TempDir())

	for _, key := range []string{
		"", "/", "..", "../etc/passwd", "a/../../b", "a//b",
		".hidden/x", "a/b/c/d", "a/ b", "a\\b", "a/..",
	} {
		_, err := ExportFilePath(key)
		assert.ErrorIs(t, err, ErrExportKeyInvalid, "key %q", key)
	}

	path, err := ExportFilePath("2026-10/BILL-2026-10-alice-admin.xlsx")
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(path))
}

func TestWriteExportFileIsAtomicAndHashed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ExportFileDirEnv, dir)

	key := "2026-10/bill.xlsx"
	size, digest, err := WriteExportFile(key, func(w io.Writer) error {
		_, err := w.Write([]byte("payload-1234"))
		return err
	})
	require.NoError(t, err)
	assert.EqualValues(t, 12, size)
	assert.Len(t, digest, 64)

	path, info, err := ExportFileStat(key)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "2026-10", "bill.xlsx"), path)
	assert.EqualValues(t, 12, info.Size())
}

func TestWriteExportFileLeavesNothingOnFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ExportFileDirEnv, dir)

	_, _, err := WriteExportFile("2026-10/bill.xlsx", func(w io.Writer) error {
		_, _ = w.Write([]byte("half"))
		return errors.New("render failed")
	})
	require.Error(t, err)

	_, _, statErr := ExportFileStat("2026-10/bill.xlsx")
	assert.ErrorIs(t, statErr, ErrExportFileMissing)

	entries, readErr := os.ReadDir(filepath.Join(dir, "2026-10"))
	require.NoError(t, readErr)
	assert.Empty(t, entries, "a failed write must not leave a partial file behind")
}

func TestExportFileStatMissing(t *testing.T) {
	t.Setenv(ExportFileDirEnv, t.TempDir())
	_, _, err := ExportFileStat("2026-11/none.xlsx")
	assert.ErrorIs(t, err, ErrExportFileMissing)
}

func withCryptoSecret(t *testing.T, secret string) {
	t.Helper()
	previous := common.CryptoSecret
	common.CryptoSecret = secret
	t.Cleanup(func() { common.CryptoSecret = previous })
}

func signExportFile(key string, expiresAt int64) string {
	mac := hmac.New(sha256.New, []byte(common.CryptoSecret))
	_, _ = mac.Write(exportFileAccessMessage(key, expiresAt))
	return strconv.FormatInt(expiresAt, 10) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestExportFileAccessBindsKeyAndExpiry(t *testing.T) {
	withCryptoSecret(t, "test-secret")

	token, err := IssueExportFileAccess("2026-10/a.xlsx", time.Hour)
	require.NoError(t, err)
	assert.True(t, VerifyExportFileAccess(token, "2026-10/a.xlsx"))
	assert.False(t, VerifyExportFileAccess(token, "2026-10/b.xlsx"), "a token is bound to one key")
	assert.False(t, VerifyExportFileAccess("", "2026-10/a.xlsx"), "no token never verifies")
	assert.False(t, VerifyExportFileAccess("garbage", "2026-10/a.xlsx"))
	assert.False(t, VerifyExportFileAccess(token, "../etc/passwd"), "invalid keys never verify")

	expired := signExportFile("2026-10/a.xlsx", time.Now().Add(-time.Minute).Unix())
	assert.False(t, VerifyExportFileAccess(expired, "2026-10/a.xlsx"), "expired tokens are rejected")

	// 篡改过期时间但不重新签名：签名覆盖 exp，等于无法自行续期。
	parts := strings.SplitN(token, ".", 2)
	require.Len(t, parts, 2)
	forged := strconv.FormatInt(time.Now().Add(48*time.Hour).Unix(), 10) + "." + parts[1]
	assert.False(t, VerifyExportFileAccess(forged, "2026-10/a.xlsx"), "the signature covers the expiry")
}

func TestIssueExportFileAccessRejectsBadInput(t *testing.T) {
	withCryptoSecret(t, "test-secret")
	_, err := IssueExportFileAccess("", time.Hour)
	assert.ErrorIs(t, err, ErrExportKeyInvalid)
	_, err = IssueExportFileAccess("2026-10/a.xlsx", 0)
	assert.ErrorIs(t, err, ErrExportFileAccessInvalid)

	withCryptoSecret(t, "")
	_, err = IssueExportFileAccess("2026-10/a.xlsx", time.Hour)
	assert.ErrorIs(t, err, ErrExportFileAccessInvalid)
}

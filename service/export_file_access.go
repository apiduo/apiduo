package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// 导出文件的访问凭据：`<过期时间戳>.<签名>`。
//
// 与任务产物那套 capability URL 同源（service/task_artifact_access.go），
// 区别只有一个：账单链接要会过期。用 HMAC(CryptoSecret) 绑定 key 与过期时间，
// 客户端无法只改一个数字就续期，也拿不到别人的文件。

const exportFileAccessVersion = "v1"

var ErrExportFileAccessInvalid = errors.New("export file access is invalid")

func exportFileAccessMessage(key string, expiresAt int64) []byte {
	return []byte(exportFileAccessVersion + "\x00export-file\x00" +
		key + "\x00" + strconv.FormatInt(expiresAt, 10))
}

// IssueExportFileAccess 生成一个在 ttl 之后失效的访问凭据。
func IssueExportFileAccess(key string, ttl time.Duration) (string, error) {
	normalized, err := normalizeExportKey(key)
	if err != nil {
		return "", err
	}
	if common.CryptoSecret == "" {
		return "", ErrExportFileAccessInvalid
	}
	if ttl <= 0 {
		return "", ErrExportFileAccessInvalid
	}
	expiresAt := time.Now().Add(ttl).Unix()

	mac := hmac.New(sha256.New, []byte(common.CryptoSecret))
	_, _ = mac.Write(exportFileAccessMessage(normalized, expiresAt))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return strconv.FormatInt(expiresAt, 10) + "." + signature, nil
}

// VerifyExportFileAccess 校验凭据是否对得上这个 key、且尚未过期。
// 空凭据直接为 false，由调用方回落到会话鉴权。
func VerifyExportFileAccess(token, key string) bool {
	if token == "" || common.CryptoSecret == "" {
		return false
	}
	normalized, err := normalizeExportKey(key)
	if err != nil {
		return false
	}
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}
	expiresAt, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || expiresAt <= 0 {
		return false
	}
	if time.Now().Unix() > expiresAt {
		return false
	}
	actual, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil || len(actual) != sha256.Size {
		return false
	}

	mac := hmac.New(sha256.New, []byte(common.CryptoSecret))
	_, _ = mac.Write(exportFileAccessMessage(normalized, expiresAt))
	return hmac.Equal(actual, mac.Sum(nil))
}

package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// 账单与导出文件的落盘约定。
//
// 一个 key 就是一段相对路径，例如 `2026-10/BILL-2026-10-alice-admin.xlsx`。
// 所有 key 都落在同一个根目录下，根目录之外的东西一概不认 —— 这里不做
// 「先拼路径再看前缀」的校验，而是逐段白名单，避免 `..` 与符号链接绕过。

const (
	// ExportFileDirEnv 让部署把文件放到挂载盘上，也让测试用临时目录隔离。
	ExportFileDirEnv = "EXPORT_FILE_DIR"
	defaultExportDir = "data/exports"

	exportKeyMaxSegments = 3
	exportKeyMaxSegment  = 128
)

// 段白名单：字母、数字、点、下划线、连字符。不含分隔符与通配符。
var exportKeySegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

var (
	ErrExportKeyInvalid  = errors.New("export file key is invalid")
	ErrExportFileMissing = errors.New("export file does not exist")
)

// ExportRoot 返回文件根目录。每次现读，便于测试与热改。
func ExportRoot() string {
	return common.GetEnvOrDefaultString(ExportFileDirEnv, defaultExportDir)
}

// normalizeExportKey 校验并归一化一个 key。返回的 key 用 `/` 分隔，
// 不含前后斜杠，供 URL 与日志使用；落盘时再用 filepath.FromSlash。
func normalizeExportKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	key = strings.Trim(key, "/")
	if key == "" {
		return "", ErrExportKeyInvalid
	}
	segments := strings.Split(key, "/")
	if len(segments) == 0 || len(segments) > exportKeyMaxSegments {
		return "", ErrExportKeyInvalid
	}
	for _, seg := range segments {
		if seg == "" || seg == "." || seg == ".." ||
			len(seg) > exportKeyMaxSegment || !exportKeySegment.MatchString(seg) {
			return "", ErrExportKeyInvalid
		}
	}
	return strings.Join(segments, "/"), nil
}

// ExportFilePath 把 key 映射到磁盘路径。调用方拿到路径前先过一遍白名单，
// 所以这里不再拼接未校验的输入。
func ExportFilePath(key string) (string, error) {
	normalized, err := normalizeExportKey(key)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(ExportRoot())
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, filepath.FromSlash(normalized))
	// 双保险：Join 之后再确认结果仍在根目录内。
	if path != root && !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return "", ErrExportKeyInvalid
	}
	return path, nil
}

// WriteExportFile 原子地写一个导出文件：先写同目录临时文件，再 rename。
// 中途失败不会留下半个文件，下载端也就不会拿到截断的账单。
func WriteExportFile(key string, write func(io.Writer) error) (size int64, digest string, err error) {
	path, err := ExportFilePath(key)
	if err != nil {
		return 0, "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, "", err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".export-*")
	if err != nil {
		return 0, "", err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()

	hasher := sha256.New()
	writer := io.MultiWriter(tmp, hasher)
	if err = write(writer); err != nil {
		_ = tmp.Close()
		return 0, "", err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return 0, "", err
	}
	if err = tmp.Close(); err != nil {
		return 0, "", err
	}
	info, statErr := os.Stat(tmpName)
	if statErr != nil {
		err = statErr
		return 0, "", err
	}
	if err = os.Rename(tmpName, path); err != nil {
		return 0, "", err
	}
	return info.Size(), hex.EncodeToString(hasher.Sum(nil)), nil
}

// ExportFileStat 返回可直接交给 http.ServeFile 的路径与元信息。
func ExportFileStat(key string) (string, os.FileInfo, error) {
	path, err := ExportFilePath(key)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, ErrExportFileMissing
		}
		return "", nil, err
	}
	if info.IsDir() {
		return "", nil, ErrExportFileMissing
	}
	return path, info, nil
}

// ExportFileETag 用大小与修改时间拼一个弱校验标识，供条件请求使用。
func ExportFileETag(info os.FileInfo) string {
	return fmt.Sprintf(`"%x-%x"`, info.Size(), info.ModTime().UnixNano())
}

package model

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// logSpool persists a log batch on local disk before the batch is handed to the
// log database. A batch is appended here first and only confirmed after the
// database insert succeeds, so a crash or a restart replays the unfinished
// batch instead of losing it.
//
// The spool keeps at most one open file. When the active file would exceed
// maxSize the writer rotates to a new file; files older than the confirmed
// position are deleted after each confirmation. File names sort in creation
// order, so a lexical comparison is also a time comparison.
type logSpool struct {
	dir     string
	maxSize int64

	mu      sync.Mutex
	file    *os.File
	name    string
	written int64
	marker  logSpoolMarker
}

const (
	logSpoolPrefix     = "logspool"
	logSpoolSuffix     = ".jsonl"
	logSpoolMarkerName = "logspool.offset"
	logSpoolDefaultMax = 64 << 20
	logSpoolScanBuffer = 1 << 20
)

// logSpoolMarker records how far the log database has accepted data. Everything
// at or after Offset in File is still unconfirmed and gets replayed on startup.
type logSpoolMarker struct {
	File   string `json:"file"`
	Offset int64  `json:"offset"`
}

func newLogSpool(dir string, maxSize int64) (*logSpool, error) {
	if maxSize <= 0 {
		maxSize = logSpoolDefaultMax
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	spool := &logSpool{dir: dir, maxSize: maxSize}
	markerPath := filepath.Join(dir, logSpoolMarkerName)
	data, err := os.ReadFile(markerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return spool, nil
		}
		return nil, err
	}
	// A corrupt marker must not stop log writing; replaying from the start of
	// the spool at worst reinserts batches that were already stored.
	_ = json.Unmarshal(data, &spool.marker)
	return spool, nil
}

// Append writes one encoded batch and returns the file it landed in together
// with the byte offset just past the batch, which is the position a caller may
// later confirm. The payload must end with a newline for every record.
func (s *logSpool) Append(payload []byte) (string, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(payload) == 0 {
		return s.name, s.written, nil
	}

	if s.file == nil || s.written+int64(len(payload)) > s.maxSize {
		if err := s.rotateLocked(); err != nil {
			return "", 0, err
		}
	}
	if _, err := s.file.Write(payload); err != nil {
		return "", 0, err
	}
	s.written += int64(len(payload))
	return s.name, s.written, nil
}

// Confirm advances the durable marker to name/offset and drops spool files that
// are fully behind it. A failed marker write leaves the previous marker in
// place, so the batch is replayed rather than lost.
func (s *logSpool) Confirm(name string, offset int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.marker.File == name && s.marker.Offset >= offset {
		return nil
	}
	s.marker = logSpoolMarker{File: name, Offset: offset}
	if err := s.writeMarkerLocked(); err != nil {
		return err
	}
	s.cleanupLocked()
	return nil
}

// ForEachPending replays every record at or after the confirmed position and
// returns the position just past the last record it read successfully. The
// caller confirms that position once the records are stored.
func (s *logSpool) ForEachPending(fn func([]byte) error) (string, int64, error) {
	s.mu.Lock()
	files, err := s.listFilesLocked()
	marker := s.marker
	s.mu.Unlock()
	if err != nil {
		return "", 0, err
	}

	var (
		lastFile   string
		lastOffset int64
	)
	for _, name := range files {
		// Files older than the confirmed position are already stored and, once
		// rotated away, deleted. Skipping them also covers a marker that points
		// at a file the cleanup already removed.
		if marker.File != "" && name < marker.File {
			continue
		}
		start := int64(0)
		if name == marker.File {
			start = marker.Offset
		}
		size, err := replaySpoolFile(filepath.Join(s.dir, name), start, fn)
		if err != nil {
			return lastFile, lastOffset, err
		}
		lastFile, lastOffset = name, size
	}
	return lastFile, lastOffset, nil
}

func (s *logSpool) rotateLocked() error {
	if s.file != nil {
		if err := s.file.Close(); err != nil {
			return err
		}
		s.file = nil
	}
	name := logSpoolPrefix + "-" + time.Now().Format("20060102T150405.000000000") + logSpoolSuffix
	file, err := os.OpenFile(filepath.Join(s.dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	s.file, s.name, s.written = file, name, 0
	return nil
}

func (s *logSpool) writeMarkerLocked() error {
	data, err := json.Marshal(s.marker)
	if err != nil {
		return err
	}
	markerPath := filepath.Join(s.dir, logSpoolMarkerName)
	tmpPath := markerPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, markerPath)
}

func (s *logSpool) cleanupLocked() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if name >= s.marker.File {
			continue
		}
		if !strings.HasPrefix(name, logSpoolPrefix+"-") || !strings.HasSuffix(name, logSpoolSuffix) {
			continue
		}
		_ = os.Remove(filepath.Join(s.dir, name))
	}
}

func (s *logSpool) listFilesLocked() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, logSpoolPrefix+"-") && strings.HasSuffix(name, logSpoolSuffix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func (s *logSpool) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// replaySpoolFile reads newline-delimited records starting at start and returns
// the offset just past the last complete record it handed to fn.
func replaySpoolFile(path string, start int64, fn func([]byte) error) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return start, nil
		}
		return start, err
	}
	defer file.Close()

	if start > 0 {
		if _, err := file.Seek(start, io.SeekStart); err != nil {
			return start, err
		}
	}
	reader := bufio.NewReaderSize(file, logSpoolScanBuffer)
	offset := start
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 1 {
			// A record without its trailing newline is a torn tail from a crash;
			// dropping it keeps the replay position on a record boundary.
			if line[len(line)-1] == '\n' {
				if err := fn(bytes.TrimSuffix(line, []byte("\n"))); err != nil {
					return offset, err
				}
				offset += int64(len(line))
			}
		}
		if readErr == io.EOF {
			return offset, nil
		}
		if readErr != nil {
			return offset, readErr
		}
	}
}

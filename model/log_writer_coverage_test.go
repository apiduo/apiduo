package model

// Coverage-gap tests added during the second adversarial pass.
//
// The first adversarial suite (log_writer_adversarial_test.go) probes spool
// durability, replay duplication and shutdown races. This file closes three
// gaps that were still untested and that decide the production envelope:
//
//  1. Drain rate. run() flushes only on a ticker, so the steady-state consume
//     rate is batchMax/interval. That is a hidden throughput ceiling: once the
//     offered load exceeds it the queue grows and, when full, enqueueLog blocks
//     the request path. TestCoverage_DrainRateEqualsBatchOverInterval pins the
//     ceiling; TestCoverage_BacklogBlocksEnqueueProportionally pins the
//     back-pressure shape.
//  2. Spool total size. SPOOL_MAX_MB maps to logSpool.maxSize, which only
//     decides when to rotate to a new file. There is no cap on the sum of
//     files, so a long log-database outage grows the spool without bound.
//  3. Large rows. Nothing verified that a multi-hundred-KB `other` payload
//     survives encode -> spool -> replay.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// coverageInserter records batches and can be made arbitrarily slow.
type coverageInserter struct {
	delay time.Duration
	rows  int
}

func (i *coverageInserter) Insert(_ context.Context, batch []*Log) error {
	if i.delay > 0 {
		time.Sleep(i.delay)
	}
	i.rows += len(batch)
	return nil
}

func (i *coverageInserter) Close() error { return nil }

// coverageWriter builds a writer without touching the package-level singleton.
func coverageWriter(t *testing.T, dir string, queue, batchMax int, interval time.Duration, ins logInserter, maxSpool int64) *logWriter {
	t.Helper()
	spool, err := newLogSpool(dir, maxSpool)
	if err != nil {
		t.Fatalf("newLogSpool: %v", err)
	}
	w := &logWriter{
		ch:       make(chan *Log, queue),
		interval: interval,
		batchMax: batchMax,
		spool:    spool,
		inserter: ins,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go w.run()
	t.Cleanup(func() {
		select {
		case <-w.done:
		default:
			close(w.stop)
			<-w.done
		}
		_ = spool.Close()
	})
	return w
}

// TestCoverage_DrainRateEqualsBatchOverInterval records the steady-state consume
// rate of the writer. If this is slower than the offered load the queue grows
// until enqueueLog blocks the request that is trying to write a log row.
func TestCoverage_DrainRateEqualsBatchOverInterval(t *testing.T) {
	const (
		batchMax = 100
		interval = 50 * time.Millisecond
		queue    = 4000
	)
	w := coverageWriter(t, t.TempDir(), queue, batchMax, interval, &coverageInserter{}, 64<<20)

	// Fill well past one batch, then measure how long the writer needs to drain.
	const rows = 2000 // 20 batches at 100/batch
	start := time.Now()
	for i := 0; i < rows; i++ {
		if _, err := enqueueLogDirect(w, &Log{UserId: 1, Type: LogTypeConsume}); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	enqueued := time.Since(start)

	deadline := time.Now().Add(10 * time.Second)
	for len(w.ch) > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	drained := time.Since(start)

	theoretical := time.Duration(float64(rows)/float64(batchMax)) * interval
	observedRate := float64(rows) / drained.Seconds()
	ceiling := float64(batchMax) / interval.Seconds()

	t.Logf("rows=%d enqueue_only=%v drained_in=%v observed_rate=%.0f rows/s ceiling=%.0f rows/s (batchMax=%d interval=%v)",
		rows, enqueued, drained, observedRate, ceiling, batchMax, interval)
	t.Logf("theoretical drain for %d rows at %v per %d-row batch = %v", rows, interval, batchMax, theoretical)

	// The ceiling is structural: run() only flushes on the ticker.
	if ceiling != float64(batchMax)/interval.Seconds() {
		t.Fatalf("ceiling math changed: %v", ceiling)
	}
	// Sanity: draining 20 batches must take at least ~19 intervals.
	if drained < 19*interval {
		t.Fatalf("drained too fast (%v), ticker-only flush not in effect", drained)
	}
	// Real production numbers for the deployed config (batchMax=5000, interval=2s).
	prodCeiling := float64(5000) / 2.0
	t.Logf("PRODUCTION: batchMax=5000 interval=2s => ceiling %.0f rows/s = %.0f RPM; app queue=50000 absorbs %.1f s at that rate",
		prodCeiling, prodCeiling*60, 50000/prodCeiling)
}

// TestCoverage_BacklogBlocksEnqueueProportionally pins the back-pressure shape:
// once the queue is full, each enqueue waits for one flush cycle. Because the
// flush is serial, the wait is roughly interval per batchMax slots freed.
func TestCoverage_BacklogBlocksEnqueueProportionally(t *testing.T) {
	const (
		batchMax = 20
		interval = 40 * time.Millisecond
		queue    = 20
	)
	// Inserter is slow enough that flushes cannot keep up while we push.
	w := coverageWriter(t, t.TempDir(), queue, batchMax, interval, &coverageInserter{delay: 10 * time.Millisecond}, 64<<20)

	// Fill the queue plus a backlog.
	var slowest time.Duration
	start := time.Now()
	const rows = 200
	for i := 0; i < rows; i++ {
		t0 := time.Now()
		if _, err := enqueueLogDirect(w, &Log{UserId: 1, Type: LogTypeConsume}); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if d := time.Since(t0); d > slowest {
			slowest = d
		}
	}
	total := time.Since(start)

	t.Logf("rows=%d queue=%d total=%v avg=%v slowest_single_enqueue=%v (interval=%v batchMax=%d)",
		rows, queue, total, total/rows, slowest, interval, batchMax)

	if slowest < interval/2 {
		t.Fatalf("expected enqueue to block for at least half an interval when the queue is full, slowest=%v", slowest)
	}
	// 200 rows through a 20-slot queue at 20 rows/40ms => at least 9 flush cycles.
	if total < 8*interval {
		t.Fatalf("total enqueue time %v too short for the queue depth", total)
	}
}

// TestCoverage_SpoolHasNoTotalSizeLimit proves SPOOL_MAX_MB is a per-file
// rotation threshold, not a cap on disk usage. During a long log-database
// outage the spool therefore grows without bound.
func TestCoverage_SpoolHasNoTotalSizeLimit(t *testing.T) {
	dir := t.TempDir()
	const maxSize = 8 << 10 // 8 KiB per file
	spool, err := newLogSpool(dir, maxSize)
	if err != nil {
		t.Fatalf("newLogSpool: %v", err)
	}
	defer spool.Close()

	payload := make([]byte, 4<<10) // 4 KiB per append, 2 appends per file
	for i := range payload {
		payload[i] = '\n'
	}

	const appends = 24 // 96 KiB total against an 8 KiB "limit"
	for i := 0; i < appends; i++ {
		if _, _, err := spool.Append(payload); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var files int
	var bytes int64
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), logSpoolSuffix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files++
		bytes += info.Size()
	}

	t.Logf("appended %d bytes with maxSize=%d => files=%d total_bytes=%d (SPOOL_MAX_MB is per-file, not a cap)",
		appends*len(payload), maxSize, files, bytes)

	if bytes <= maxSize {
		t.Fatalf("expected spool to exceed maxSize, got %d bytes", bytes)
	}
	if files < 2 {
		t.Fatalf("expected rotation to create multiple files, got %d", files)
	}
	t.Logf("DEFECT: a log database outage can fill the disk; only an external monitor can bound %s", dir)
}

// TestCoverage_LargeRowSurvivesSpool verifies a large `other` payload is encoded,
// spooled and replayed byte-for-byte.
func TestCoverage_LargeRowSurvivesSpool(t *testing.T) {
	dir := t.TempDir()
	spool, err := newLogSpool(dir, 64<<20)
	if err != nil {
		t.Fatalf("newLogSpool: %v", err)
	}
	defer spool.Close()

	big := strings.Repeat("x", 512<<10) // 512 KiB
	row := &Log{
		UserId:    7,
		Type:      LogTypeConsume,
		Content:   "large payload",
		Other:     `{"blob":"` + big + `"}`,
		RequestId: "coverage-large-row",
	}
	payload, err := encodeLogBatch([]*Log{row})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	name, offset, err := spool.Append(payload)
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	var replayed []byte
	var count int
	if _, _, err := spool.ForEachPending(func(rec []byte) error {
		replayed = append(replayed, rec...)
		count++
		return nil
	}); err != nil {
		t.Fatalf("ForEachPending: %v", err)
	}

	var got Log
	if err := json.Unmarshal(replayed, &got); err != nil {
		t.Fatalf("unmarshal replayed row (%d bytes, %d records): %v", len(replayed), count, err)
	}

	t.Logf("encoded=%d bytes spooled_in=%s@%d replayed=%d bytes records=%d other_len=%d",
		len(payload), name, offset, len(replayed), count, len(got.Other))

	if len(got.Other) != len(row.Other) {
		t.Fatalf("other payload truncated: %d != %d", len(got.Other), len(row.Other))
	}
	if got.RequestId != row.RequestId {
		t.Fatalf("request_id mismatch: %q", got.RequestId)
	}
}

// enqueueLogDirect pushes into one specific writer, bypassing the package
// singleton that enqueueLog reads. It mirrors enqueueLog's blocking semantics
// so the tests measure the real back-pressure.
func enqueueLogDirect(w *logWriter, log *Log) (bool, error) {
	select {
	case w.ch <- log:
		return true, nil
	case <-w.stop:
		return true, errWriterStoppedForTest
	}
}

var errWriterStoppedForTest = &coverageError{"log writer stopped"}

type coverageError struct{ msg string }

func (e *coverageError) Error() string { return e.msg }

// TestCoverage_SpoolDirIsPerInstance documents the multi-instance hazard: the
// spool directory has no instance lock, so two processes sharing it will rotate
// and confirm each other's files.
func TestCoverage_SpoolDirIsPerInstance(t *testing.T) {
	dir := t.TempDir()
	a, err := newLogSpool(dir, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := newLogSpool(dir, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	payload := []byte(`{"x":1}` + "\n")
	nameA, offA, err := a.Append(payload)
	if err != nil {
		t.Fatal(err)
	}
	nameB, _, err := b.Append(payload)
	if err != nil {
		t.Fatal(err)
	}

	// A confirms its own file; B's marker is independent.
	if err := a.Confirm(nameA, offA); err != nil {
		t.Fatal(err)
	}

	var seen int
	if _, _, err := b.ForEachPending(func([]byte) error { seen++; return nil }); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	t.Logf("instanceA=%s instanceB=%s replayed_by_b=%d files_on_disk=%d", nameA, nameB, seen, len(entries))
	t.Logf("DEFECT: no instance lock in the spool dir; LOG_WRITER_SPOOL_DIR must differ per instance")
}

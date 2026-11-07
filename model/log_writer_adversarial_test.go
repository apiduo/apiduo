package model

// Adversarial review suite for the apiduo asynchronous log writer.
//
// The existing log_writer_test.go covers the happy path only. This file probes
// the failure modes that decide whether the writer can be trusted in
// production: back-pressure, shutdown races, spool durability, replay
// duplication, column drift and multi-instance spool sharing.
//
// NOTE: several tests below *record the current (defective) behaviour* instead
// of asserting the desired one. They are deliberately green so CI can run
// them; the observed values are printed with t.Logf and cited in
// docs/apiduo/log-writer-review.md. Once a defect is fixed, flip the assertion
// to the desired behaviour.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

type adversarialInserter struct {
	insertErr error
	calls     int
	rows      [][]*Log
}

func (i *adversarialInserter) Insert(_ context.Context, batch []*Log) error {
	i.calls++
	if i.insertErr != nil {
		return i.insertErr
	}
	i.rows = append(i.rows, append([]*Log(nil), batch...))
	return nil
}

func (i *adversarialInserter) Close() error { return nil }

// adversarialNoopInserter is used by benchmarks: it does not retain rows, so
// a long benchmark run cannot exhaust memory.
type adversarialNoopInserter struct{ rows int }

func (i *adversarialNoopInserter) Insert(_ context.Context, batch []*Log) error {
	i.rows += len(batch)
	return nil
}

func (i *adversarialNoopInserter) Close() error { return nil }

func withAdversarialWriter(t *testing.T, w *logWriter, fn func()) {
	t.Helper()
	apiduoLogWriterMu.Lock()
	previous := apiduoLogWriter
	apiduoLogWriter = w
	apiduoLogWriterMu.Unlock()
	t.Cleanup(func() {
		apiduoLogWriterMu.Lock()
		apiduoLogWriter = previous
		apiduoLogWriterMu.Unlock()
	})
	fn()
}

type enqueueResult struct {
	handled bool
	err     error
}

// ---------------------------------------------------------------------------
// 1. Back-pressure: a full queue blocks the request path
// ---------------------------------------------------------------------------

// The commit message promises "queue full falls back to synchronous insert
// instead of blocking the request". The implementation has no default branch:
// enqueueLog blocks until the writer consumes a row or stop is closed. When the
// log database is slow (flush has a 60s timeout), every API request that emits
// a log stalls behind it.
func TestAdversarial_EnqueueFallsBackWhenQueueIsFull(t *testing.T) {
	w := &logWriter{
		ch:       make(chan *Log, 1),
		batchMax: 10,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		inserter: &adversarialInserter{},
	}
	withAdversarialWriter(t, w, func() {
		previous := logWriterEnqueueWait
		logWriterEnqueueWait = 80 * time.Millisecond
		defer func() { logWriterEnqueueWait = previous }()

		w.ch <- &Log{UserId: 1} // fill the only slot; no run() goroutine -> simulates a stuck flush

		start := time.Now()
		handled, err := enqueueLog(&Log{UserId: 2})
		elapsed := time.Since(start)

		assert.False(t, handled, "队列满时必须把行交回调用方同步插入；返回 handled=true 会让 createLog 丢掉该行")
		assert.NoError(t, err, "降级不是错误：createLog 会落到同步插入")
		assert.GreaterOrEqual(t, elapsed, 60*time.Millisecond, "应先有界等待，不能立刻放弃")
		assert.Less(t, elapsed, 2*time.Second, "等待必须有上界，不能随 CH 恢复时间无限拉长")
		assert.EqualValues(t, 1, w.dropped.Load(), "降级次数应被计数，便于排障")
	})
}

// ---------------------------------------------------------------------------
// 2. Shutdown race: enqueue after stop is non-deterministic
// ---------------------------------------------------------------------------

// ShutdownLogWriter sets apiduoLogWriter=nil, closes stop, then waits for run()
// to drain. Any in-flight request that already copied the writer pointer can
// still call enqueueLog. With both select cases ready, Go picks at random:
//   - ch <- log  => the row is accepted but run() may have returned => lost
//   - <-stop     => handled=true with an error => createLog drops the row
func TestAdversarial_EnqueueAfterStopFallsBack(t *testing.T) {
	w := &logWriter{
		ch:       make(chan *Log, 8),
		batchMax: 10,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		inserter: &adversarialInserter{},
	}
	close(w.stop)

	withAdversarialWriter(t, w, func() {
		var accepted int
		for i := range 1000 {
			handled, err := enqueueLog(&Log{UserId: i})
			assert.NoError(t, err)
			if handled {
				accepted++
			}
			for len(w.ch) > 0 {
				<-w.ch
			}
		}
		assert.Zero(t, accepted, "stop 之后不应再有任何行被认为是 writer 接管（会丢行）")
		assert.Zero(t, len(w.ch), "stop 之后不应再有行进入队列")
	})
}

// ---------------------------------------------------------------------------
// 3. Spool write failure + insert failure => silent row loss on restart
// ---------------------------------------------------------------------------

// flush() continues to Insert even when spool.Append failed. If the insert also
// fails, the rows live only in w.pending (memory). The retry path claims they
// "remain in the spool for replay" -- they do not.
func TestAdversarial_SpoolFailurePlusInsertFailureLosesRowsOnRestart(t *testing.T) {
	dir := t.TempDir()
	spool, err := newLogSpool(dir, 1<<20)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(dir), "make subsequent spool.Append fail")

	failing := &adversarialInserter{insertErr: errors.New("log database unavailable")}
	w := &logWriter{
		ch:       make(chan *Log, 8),
		batchMax: 50,
		spool:    spool,
		inserter: failing,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	w.ch <- &Log{UserId: 42, RequestId: "row-only-in-memory", Type: LogTypeConsume}
	w.flush(false)

	require.Len(t, w.pending, 1, "failed batch is kept in memory")
	assert.Equal(t, 1, failing.calls, "insert was attempted despite spool failure")

	// Simulate a restart: the directory is recreated and replay runs.
	spool2, err := newLogSpool(dir, 1<<20)
	require.NoError(t, err)
	w2 := &logWriter{
		ch:       make(chan *Log, 8),
		batchMax: 50,
		spool:    spool2,
		inserter: &adversarialInserter{},
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	replayed, err := w2.replay()
	require.NoError(t, err)
	t.Logf("证据：spool 写失败 + insert 失败后，内存中 %d 行在重启回放时只剩 %d 行", len(w.pending), replayed)
	assert.Zero(t, replayed, "没有任何行落盘 => 重启即永久丢失")
}

// ---------------------------------------------------------------------------
// 4. Column contract: DDL order vs native INSERT order
// ---------------------------------------------------------------------------

// Both lists are hard-coded strings in two different files. A third party
// adding a column to either side silently shifts every subsequent field.
// The real-ClickHouse round trip test is skipped unless TEST_CLICKHOUSE_DSN is
// set, so this pure-string check is the only CI-visible guard.
func adversarialDDLColumns(ddl string) []string {
	var cols []string
	for _, line := range strings.Split(ddl, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == ")" || strings.HasPrefix(line, "CREATE") ||
			strings.HasPrefix(line, "ENGINE") || strings.HasPrefix(line, "PARTITION") ||
			strings.HasPrefix(line, "ORDER BY") || strings.HasPrefix(line, "TTL") ||
			strings.HasPrefix(line, "SETTINGS") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		cols = append(cols, strings.Trim(fields[0], "`,"))
	}
	return cols
}

func adversarialInsertColumns(stmt string) []string {
	inner := strings.TrimPrefix(stmt, "INSERT INTO logs (")
	inner = strings.TrimSuffix(inner, ")")
	var cols []string
	for _, part := range strings.Split(inner, ",") {
		cols = append(cols, strings.Trim(strings.TrimSpace(part), "`"))
	}
	return cols
}

func TestAdversarial_DDLAndNativeInsertColumnOrderAgree(t *testing.T) {
	ddlCols := adversarialDDLColumns(clickHouseLogCreateTableSQL(0))
	insertCols := adversarialInsertColumns(clickHouseLogInsertStatement)
	require.Len(t, ddlCols, 20)
	t.Logf("DDL 列(%d): %v", len(ddlCols), ddlCols)
	assert.Equal(t, ddlCols, insertCols, "两处硬编码列顺序必须逐字一致，否则 native batch 会整体错位")
}

// The GORM synchronous path derives columns from the Log struct field order.
// Verify that order (minus the read-only ChannelName) still matches the DDL.
func TestAdversarial_LogStructFieldOrderMatchesDDLColumns(t *testing.T) {
	ddlCols := adversarialDDLColumns(clickHouseLogCreateTableSQL(0))
	// Struct order from model/log.go; ChannelName is gorm:"->" (read only).
	structCols := []string{
		"id", "user_id", "created_at", "type", "content", "username", "token_name",
		"model_name", "quota", "prompt_tokens", "completion_tokens", "use_time",
		"is_stream", "channel_id", "token_id", "group", "ip", "request_id",
		"upstream_request_id", "other",
	}
	assert.Equal(t, ddlCols, structCols, "Log 结构体字段顺序 = GORM 同步插入的列顺序 = DDL 列顺序")
}

// ---------------------------------------------------------------------------
// 5. Type contract: silent int32 truncation
// ---------------------------------------------------------------------------

// Log counters are Go int (64-bit on the server); the CH table declares Int32
// and the native inserter explicitly casts with int32(...). There is no range
// check anywhere, so a sufficiently large value wraps instead of failing.
func TestAdversarial_Int32CastSilentlyTruncates(t *testing.T) {
	large := int64(1)<<33 + 7 // runtime value: a compile-time constant would overflow int32
	got := int32(large)
	t.Logf("证据：Log.Quota/PromptTokens/UseTime 为 Go int，DDL 为 Int32；%d 被 int32() 静默截成 %d", large, got)
	assert.NotEqualValues(t, large, got)
	assert.EqualValues(t, 7, got)
}

// ---------------------------------------------------------------------------
// 6. at-least-once: replay re-inserts rows; the engine and FINAL collapse them
// ---------------------------------------------------------------------------

// Replaying a batch whose marker was lost is inherent to at-least-once. What
// must not happen is those replays reaching sum(quota), so the ClickHouse table
// is ReplacingMergeTree keyed on (created_at, request_id) and every aggregation
// reads it with FINAL. A SQLite test can pin the replay itself but not the
// engine, so the DDL assertion below covers that half.
func TestAdversarial_ReplayReinsertsRowsAndEngineCollapsesThem(t *testing.T) {
	setupLogWriterTestDB(t)
	dir := t.TempDir()

	spool, err := newLogSpool(dir, 1<<20)
	require.NoError(t, err)
	payload, err := encodeLogBatch([]*Log{
		{UserId: 1, CreatedAt: 100, Type: LogTypeConsume, Quota: 100, RequestId: "dup-1"},
		{UserId: 2, CreatedAt: 101, Type: LogTypeConsume, Quota: 100, RequestId: "dup-2"},
		{UserId: 3, CreatedAt: 102, Type: LogTypeConsume, Quota: 100, RequestId: "dup-3"},
	})
	require.NoError(t, err)
	_, _, err = spool.Append(payload)
	require.NoError(t, err)
	require.NoError(t, spool.Close())

	w := newTestLogWriter(t, dir, 10)
	first, err := w.replay()
	require.NoError(t, err)
	require.EqualValues(t, 3, first)
	require.EqualValues(t, 3, countStoredLogs(t))

	// Simulate "insert succeeded but marker write was lost" (crash window).
	w.spool.marker = logSpoolMarker{}
	second, err := w.replay()
	require.NoError(t, err)
	require.EqualValues(t, 3, second)

	var sum int64
	require.NoError(t, LOG_DB.Table("logs").Select("COALESCE(sum(quota), 0)").Scan(&sum).Error)
	assert.EqualValues(t, 6, countStoredLogs(t), "重放会再次插入同一批行，这是 at-least-once 的固有尾巴")
	assert.EqualValues(t, 600, sum)

	// Nothing above keeps duplicates out of the numbers — the engine does.
	replacing, err := replacingLogTableDDL("logs", 0)
	require.NoError(t, err)
	assert.Contains(t, replacing, "ENGINE = ReplacingMergeTree()",
		"重放去重依赖 ReplacingMergeTree；改回 MergeTree 会让 sum(quota) 直接翻倍")
}

// ---------------------------------------------------------------------------
// 7. Multi-instance: a shared spool directory corrupts both instances
// ---------------------------------------------------------------------------

// LOG_WRITER_SPOOL_DIR defaults to the relative "data/log_spool". Two replicas
// mounting the same volume share one marker file; Confirm() deletes every
// spool file sorting before its own marker, including the other replica's
// unconfirmed file.
func TestAdversarial_TwoInstancesSharingSpoolDirectoryLoseRows(t *testing.T) {
	dir := t.TempDir()
	s1, err := newLogSpool(dir, 1<<20)
	require.NoError(t, err)
	name1, _, err := s1.Append([]byte("row-from-replica-A\n"))
	require.NoError(t, err)

	time.Sleep(2 * time.Millisecond) // guarantee name1 < name2 in creation order
	s2, err := newLogSpool(dir, 1<<20)
	require.NoError(t, err)
	name2, offset2, err := s2.Append([]byte("row-from-replica-B\n"))
	require.NoError(t, err)
	require.Less(t, name1, name2)

	// Replica B confirms its own batch.
	require.NoError(t, s2.Confirm(name2, offset2))

	var lines []string
	_, _, err = s1.ForEachPending(func(line []byte) error {
		lines = append(lines, string(line))
		return nil
	})
	require.NoError(t, err)
	t.Logf("证据：A 的 spool 文件被 B 的 Confirm 删除；A 重启后回放内容 = %v", lines)
	assert.NotContains(t, lines, "row-from-replica-A", "A 未确认的行被 B 删掉")
	assert.Contains(t, lines, "row-from-replica-B", "A 反而会重复插入 B 已确认的行")
}

// ---------------------------------------------------------------------------
// 8. Spool robustness: torn tail and corrupt marker
// ---------------------------------------------------------------------------

func TestAdversarial_SpoolSkipsTornTail(t *testing.T) {
	dir := t.TempDir()
	name := logSpoolPrefix + "-20261107T000000.000000000" + logSpoolSuffix
	complete := `{"user_id":1}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, name),
		[]byte(complete+"\n"+`{"user_id":2` /* no trailing newline */), 0o644))

	s, err := newLogSpool(dir, 1<<20)
	require.NoError(t, err)
	var lines []string
	_, offset, err := s.ForEachPending(func(line []byte) error {
		lines = append(lines, string(line))
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{complete}, lines)
	assert.EqualValues(t, len(complete)+1, offset, "确认位点必须停在完整记录边界")
}

func TestAdversarial_CorruptMarkerFallsBackToFullReplay(t *testing.T) {
	dir := t.TempDir()
	name := logSpoolPrefix + "-20261107T000000.000000000" + logSpoolSuffix
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("a\nb\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, logSpoolMarkerName), []byte("{not-json"), 0o644))

	s, err := newLogSpool(dir, 1<<20)
	require.NoError(t, err, "损坏的 marker 不得阻断启动")
	var lines []string
	_, _, err = s.ForEachPending(func(line []byte) error {
		lines = append(lines, string(line))
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, lines, "marker 损坏时从头回放（可能重复，符合 at-least-once）")
}

// ---------------------------------------------------------------------------
// 9. Default-off equivalence
// ---------------------------------------------------------------------------

// StartLogWriter must be a no-op for non-ClickHouse log databases and when the
// switch is off. apiduo.cn runs SQLite with no LOG_SQL_DSN, so none of the
// writer code is reachable there.
func TestAdversarial_StartLogWriterNoOpWithoutClickHouse(t *testing.T) {
	var previous common.DatabaseType
	for _, dt := range []common.DatabaseType{
		common.DatabaseTypeSQLite, common.DatabaseTypeMySQL,
		common.DatabaseTypePostgreSQL, common.DatabaseTypeClickHouse,
	} {
		if common.UsingLogDatabase(dt) {
			previous = dt
		}
	}
	t.Cleanup(func() { common.SetLogDatabaseType(previous) })

	t.Setenv("LOG_WRITER_ASYNC", "true")
	common.SetLogDatabaseType(common.DatabaseTypeSQLite)
	require.NoError(t, StartLogWriter())
	assert.Nil(t, apiduoLogWriter, "非 ClickHouse 日志库时不得启动写入器")

	common.SetLogDatabaseType(common.DatabaseTypeClickHouse)
	// Leave LOG_SQL_DSN empty so newClickHouseLogInserter would fail if reached;
	// with the switch off the function must still be a no-op.
	t.Setenv("LOG_WRITER_ASYNC", "false")
	require.NoError(t, StartLogWriter())
	assert.Nil(t, apiduoLogWriter, "开关关闭时行为与上游完全一致")
}

// The spool payload is JSON; a malformed record must be skipped without
// blocking later records. Corrupt rows are dropped, not retried.
func TestAdversarial_ReplaySkipsCorruptRecord(t *testing.T) {
	dir := t.TempDir()
	name := logSpoolPrefix + "-20261107T000000.000000000" + logSpoolSuffix
	good, err := json.Marshal(&Log{UserId: 9, RequestId: "good", Type: LogTypeConsume})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name),
		append(append([]byte("{"+"broken\n"), append(good, '\n')...), '\n'), 0o644))

	s, err := newLogSpool(dir, 1<<20)
	require.NoError(t, err)
	w := &logWriter{
		ch:       make(chan *Log, 8),
		batchMax: 10,
		spool:    s,
		inserter: &adversarialInserter{},
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	replayed, err := w.replay()
	require.NoError(t, err)
	t.Logf("证据：坏记录被跳过且不计入 replay 计数，replayed=%d（坏行永久丢失）", replayed)
	assert.EqualValues(t, 1, replayed)
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

func adversarialRows(n int) []*Log {
	rows := make([]*Log, n)
	now := time.Now().Unix()
	for i := range rows {
		rows[i] = &Log{
			UserId: i + 1, CreatedAt: now, Type: LogTypeConsume,
			Content: "adversarial benchmark row", Username: "bench", TokenName: "tk",
			ModelName: "gpt-bench", Quota: 10, PromptTokens: 100, CompletionTokens: 50,
			UseTime: 3, IsStream: true, ChannelId: 1, TokenId: 2, Group: "vip",
			Ip: "127.0.0.1", RequestId: fmt.Sprintf("bench-%d", i),
			Other: `{"cache_tokens":0,"model_ratio":1}`,
		}
	}
	return rows
}

func BenchmarkAdversarial_EncodeLogBatch(b *testing.B) {
	for _, size := range []int{1, 200, 2000} {
		b.Run(fmt.Sprintf("rows=%d", size), func(b *testing.B) {
			rows := adversarialRows(size)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := encodeLogBatch(rows); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkAdversarial_SpoolAppend(b *testing.B) {
	dir := b.TempDir()
	// A small maxSize forces a rotation per iteration; Confirm then deletes the
	// previous file, keeping disk usage bounded across a long benchmark run.
	s, err := newLogSpool(dir, 32<<10)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	payload, err := encodeLogBatch(adversarialRows(200))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		name, offset, err := s.Append(payload)
		if err != nil {
			b.Fatal(err)
		}
		if err := s.Confirm(name, offset); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.N*200)/b.Elapsed().Seconds(), "rows/s")
}

func BenchmarkAdversarial_AsyncFlushBatch(b *testing.B) {
	const batchSize = 2000
	dir := b.TempDir()
	// Rotate + Confirm every iteration so the spool does not grow without bound.
	s, err := newLogSpool(dir, 400<<10)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	rows := adversarialRows(batchSize)
	w := &logWriter{
		ch:       make(chan *Log, batchSize),
		batchMax: batchSize,
		spool:    s,
		inserter: &adversarialNoopInserter{},
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for _, row := range rows {
			w.ch <- row
		}
		w.flush(false)
	}
	b.ReportMetric(float64(b.N*batchSize)/b.Elapsed().Seconds(), "rows/s")
}

func BenchmarkAdversarial_EnqueueOnly(b *testing.B) {
	w := &logWriter{
		ch:       make(chan *Log, 4096),
		batchMax: 2000,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		inserter: &adversarialNoopInserter{},
	}
	withAdversarialBenchWriter(b, w)
	row := &Log{UserId: 1, Type: LogTypeConsume}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if len(w.ch) == cap(w.ch) {
			b.StopTimer()
			for len(w.ch) > 0 {
				<-w.ch
			}
			b.StartTimer()
		}
		if _, err := enqueueLog(row); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkAdversarial_SyncCreateSingle measures the upstream synchronous
// path (one GORM Create per log row) on SQLite. It is not a ClickHouse number:
// it exists to quantify the per-request DB write that the async writer removes.
func BenchmarkAdversarial_SyncCreateSingle(b *testing.B) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		b.Fatal(err)
	}
	if err := db.AutoMigrate(&Log{}); err != nil {
		b.Fatal(err)
	}
	rows := adversarialRows(1)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		row := *rows[0]
		if err := db.Create(&row).Error; err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "rows/s")
}

func withAdversarialBenchWriter(b *testing.B, w *logWriter) {
	b.Helper()
	apiduoLogWriterMu.Lock()
	previous := apiduoLogWriter
	apiduoLogWriter = w
	apiduoLogWriterMu.Unlock()
	b.Cleanup(func() {
		apiduoLogWriterMu.Lock()
		apiduoLogWriter = previous
		apiduoLogWriterMu.Unlock()
	})
}

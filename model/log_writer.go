package model

import (
	"bytes"
	"context"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// The asynchronous log writer decouples request handling from log persistence.
//
// Every log row used to be inserted synchronously on the request path. With a
// ClickHouse log database that insert waits for the server side async-insert
// buffer to flush, so a slow or busy log database directly stalls API requests.
// The writer moves that wait onto one background goroutine and keeps a local
// write-ahead copy of each batch, so a restart replays unfinished batches
// instead of losing rows that reconciliation depends on.
//
// The writer inserts through the native ClickHouse batch API rather than
// GORM: the GORM ClickHouse driver expands a batch into one prepared statement
// and executes it row by row, which costs one round trip per row and re-enters
// the async-insert buffer for every row. A native batch sends the whole batch
// as one request.
//
// Ordering guarantees: rows reach the log database in enqueue order, possibly
// late by one batch interval. Delivery is at-least-once - a crash between the
// insert and the marker write replays that batch, so reconciliation queries
// must de-duplicate on request_id.

const (
	logWriterDefaultInterval = 5 * time.Second
	logWriterDefaultBatch    = 2000
	logWriterDefaultQueue    = 100000
	logWriterMaxBatch        = 50000
	logWriterMaxInterval     = 60 * time.Second
	// How long a caller waits for queue space before falling back to the
	// synchronous insert path.
	logWriterDefaultEnqueueWait = 1000 // milliseconds

	clickHouseLogInsertStatement = "INSERT INTO logs (" +
		"id, user_id, created_at, type, content, username, token_name, model_name, " +
		"quota, prompt_tokens, completion_tokens, use_time, is_stream, channel_id, token_id, " +
		"`group`, ip, request_id, upstream_request_id, other)"
)

// These are variables rather than constants so the stall and heartbeat paths can
// be exercised in seconds instead of minutes.
var (
	// logWriterFlushTimeout bounds one insert handoff. A batch that is not
	// acknowledged inside this budget is abandoned to the spool and retried,
	// instead of holding the flush loop.
	logWriterFlushTimeout = 60 * time.Second
	// logWriterStallRounds is how many consecutive rounds must fail before the
	// connection is treated as dead and rebuilt. At the default two second
	// interval this is single-digit seconds: long enough to ride out one slow
	// insert, short enough that a wedged connection never parks the queue.
	logWriterStallRounds = 3
	// logWriterHeartbeatInterval is how often the writer prints its counters.
	// A quiet site still needs proof that the writer is alive: without it,
	// "nothing to log" and "writer stopped" look identical from outside.
	logWriterHeartbeatInterval = 5 * time.Minute
)

// logInserter stores a batch of rows. It exists so the writer can be exercised
// against a test database while production uses the native ClickHouse batch.
type logInserter interface {
	Insert(ctx context.Context, rows []*Log) error
	Close() error
}

type logWriter struct {
	ch       chan *Log
	interval time.Duration
	batchMax int
	spool    *logSpool
	dsn      string

	// inserterMu guards inserter: the flush loop reads it while a stall
	// recovery may swap in a new connection.
	inserterMu sync.Mutex
	inserter   logInserter

	// busy is the "one insert in flight" token. Insert can block for a long
	// time on a half-open connection, so a blocking wait here would wedge the
	// flush loop instead of just the current batch. Losing the race means this
	// round is skipped and its rows stay in pending; it never means starting a
	// second insert goroutine.
	busy atomic.Bool
	// gen invalidates an insert that was already in flight when the connection
	// was rebuilt: it must not clear the token its successor now holds.
	gen atomic.Int64

	// stalled counts consecutive rounds that could not hand a batch over.
	stalled int

	lastBeat time.Time

	stop chan struct{}
	done chan struct{}

	// pending holds a batch whose insert failed. It is retried with the next
	// flush so a transient log-database error does not lose the rows.
	pending []*Log

	// spoolDirty records that the last disk append failed. Those rows then live
	// only in pending, so the next flush has to write the whole batch again —
	// otherwise a restart loses whatever never reached the spool.
	spoolDirty bool

	written  atomic.Int64
	replayed atomic.Int64
	failed   atomic.Int64
	// dropped counts rows handed back to the synchronous path because the queue
	// stayed full for the whole wait. They are stored, not lost.
	dropped atomic.Int64
}

var (
	apiduoLogWriter   *logWriter
	apiduoLogWriterMu sync.Mutex

	// logWriterEnqueueWait bounds the queue-full wait. Zero or negative values
	// fall back to the default so a typo cannot reintroduce the unbounded block.
	logWriterEnqueueWait = func() time.Duration {
		ms := common.GetEnvOrDefault("LOG_WRITER_ENQUEUE_WAIT_MS", logWriterDefaultEnqueueWait)
		if ms <= 0 {
			ms = logWriterDefaultEnqueueWait
		}
		return time.Duration(ms) * time.Millisecond
	}()
)

// StartLogWriter enables asynchronous log persistence for the ClickHouse log
// database. It is a no-op for other log databases and when LOG_WRITER_ASYNC is
// not enabled, so the default behaviour stays identical to upstream.
func StartLogWriter() error {
	if !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return nil
	}
	if !common.GetEnvOrDefaultBool("LOG_WRITER_ASYNC", false) {
		return nil
	}

	inserter, err := newClickHouseLogInserter(os.Getenv("LOG_SQL_DSN"))
	if err != nil {
		return err
	}

	// 写入走 native 客户端、查询走 GORM，两者对同一条 DSN 的库名解析并不一致：
	// native 认 ?database= 查询参数，database/sql driver 只认路径段。同一条
	// DSN 因此可以「写进 A 库、查 B 库」，表现就是日志页永远空白而磁盘上一直
	// 有数据。启动时把两边实际落点都打出来，不一致直接报错 —— 这个故障不能
	// 靠人去猜。
	if inserter.database != "" {
		var queryDB string
		if err := LOG_DB.Raw("SELECT currentDatabase()").Scan(&queryDB).Error; err == nil && queryDB != "" {
			if queryDB != inserter.database {
				common.SysError(common.LogText(
					"log database split: writer inserts into %q but queries read %q; rows will be invisible until LOG_SQL_DSN agrees with itself",
					inserter.database, queryDB))
			} else {
				common.SysLog(common.LogText("log database verified: writer and queries both use %q", queryDB))
			}
		}
	}

	dir := common.GetEnvOrDefaultString("LOG_WRITER_SPOOL_DIR", "data/log_spool")
	spool, err := newLogSpool(dir, int64(common.GetEnvOrDefault("LOG_WRITER_SPOOL_MAX_MB", 64))<<20)
	if err != nil {
		_ = inserter.Close()
		return err
	}

	intervalMs := common.GetEnvOrDefault("LOG_WRITER_INTERVAL_MS", int(logWriterDefaultInterval/time.Millisecond))
	if intervalMs <= 0 || time.Duration(intervalMs)*time.Millisecond > logWriterMaxInterval {
		intervalMs = int(logWriterDefaultInterval / time.Millisecond)
	}
	batchMax := common.GetEnvOrDefault("LOG_WRITER_BATCH_SIZE", logWriterDefaultBatch)
	if batchMax <= 0 || batchMax > logWriterMaxBatch {
		batchMax = logWriterDefaultBatch
	}
	queueSize := common.GetEnvOrDefault("LOG_WRITER_QUEUE_SIZE", logWriterDefaultQueue)
	if queueSize <= 0 {
		queueSize = logWriterDefaultQueue
	}

	writer := &logWriter{
		ch:       make(chan *Log, queueSize),
		interval: time.Duration(intervalMs) * time.Millisecond,
		batchMax: batchMax,
		spool:    spool,
		dsn:      os.Getenv("LOG_SQL_DSN"),
		inserter: inserter,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}

	replayed, err := writer.replay()
	if err != nil {
		// Replay failure must not prevent startup: the rows stay on disk and the
		// next restart tries again.
		common.SysError(common.LogText("log writer replay failed: %s", err.Error()))
	}

	apiduoLogWriterMu.Lock()
	apiduoLogWriter = writer
	apiduoLogWriterMu.Unlock()

	go writer.run()

	common.SysLog(common.LogText("log writer enabled: dir=%s interval=%s batch=%d queue=%d enqueue_wait=%s replayed=%d",
		dir, writer.interval, batchMax, queueSize, logWriterEnqueueWait, replayed))
	return nil
}

// ShutdownLogWriter flushes everything still queued or pending and closes the
// spool. Call it after the HTTP server stopped accepting requests so no new
// rows are enqueued while it runs.
func ShutdownLogWriter() {
	apiduoLogWriterMu.Lock()
	writer := apiduoLogWriter
	apiduoLogWriter = nil
	apiduoLogWriterMu.Unlock()
	if writer == nil {
		return
	}

	close(writer.stop)
	<-writer.done

	stats := writer.stats()
	common.SysLog(common.LogText("log writer stopped: written=%d replayed=%d failed=%d fallback=%d",
		stats.Written, stats.Replayed, stats.Failed, stats.Dropped))
	if err := writer.spool.Close(); err != nil {
		common.SysError(common.LogText("log spool close failed: %s", err.Error()))
	}
	if err := writer.inserter.Close(); err != nil {
		common.SysError(common.LogText("log inserter close failed: %s", err.Error()))
	}
}

// enqueueLog hands a row to the background writer. It reports whether the
// writer took ownership; a false result means the caller must insert directly,
// which keeps the synchronous upstream path in charge when the writer is off.
func enqueueLog(log *Log) (bool, error) {
	apiduoLogWriterMu.Lock()
	writer := apiduoLogWriter
	apiduoLogWriterMu.Unlock()
	if writer == nil {
		return false, nil
	}

	// Shutdown wins over a free slot. After stop the writer goroutine is gone,
	// and reporting "handled" here dropped the row on the floor; handing it back
	// lets the caller fall through to the synchronous insert.
	select {
	case <-writer.stop:
		return false, nil
	default:
	}

	select {
	case writer.ch <- log:
		return true, nil
	default:
	}

	// A full queue means the log database has not accepted a batch for a while.
	// Wait a bounded amount of time, then hand the row back for a synchronous
	// insert: blocking without a bound turned a slow log database into a
	// request outage, and nothing in the writer may lose the row.
	timer := time.NewTimer(logWriterEnqueueWait)
	defer timer.Stop()
	select {
	case writer.ch <- log:
		return true, nil
	case <-writer.stop:
		return false, nil
	case <-timer.C:
		dropped := writer.dropped.Add(1)
		if dropped&63 == 1 {
			common.SysError(common.LogText(
				"log writer queue full for %s; %d row(s) fell back to the synchronous path",
				logWriterEnqueueWait, dropped))
		}
		return false, nil
	}
}

type logWriterStats struct {
	Written  int64
	Replayed int64
	Failed   int64
	Dropped  int64
}

func (w *logWriter) stats() logWriterStats {
	return logWriterStats{
		Written:  w.written.Load(),
		Replayed: w.replayed.Load(),
		Failed:   w.failed.Load(),
		Dropped:  w.dropped.Load(),
	}
}

func (w *logWriter) run() {
	defer close(w.done)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	beat := time.NewTicker(logWriterHeartbeatInterval)
	defer beat.Stop()

	for {
		select {
		case <-ticker.C:
			w.flush(false)
		case <-beat.C:
			w.heartbeat()
		case <-w.stop:
			// Drain what is already queued so a rolling restart keeps every row
			// that reached the writer.
			for len(w.ch) > 0 {
				w.flush(false)
			}
			w.flush(true)
			return
		}
	}
}

// collect takes up to batchMax rows without blocking, so a partially filled
// batch never waits for the next tick.
func (w *logWriter) collect() []*Log {
	batch := make([]*Log, 0, min(w.batchMax, len(w.ch)))
	for range w.batchMax {
		select {
		case log := <-w.ch:
			batch = append(batch, log)
		default:
			return batch
		}
	}
	return batch
}

func (w *logWriter) flush(final bool) {
	batch := w.collect()
	if len(batch) == 0 {
		if !final || len(w.pending) == 0 {
			return
		}
		batch = nil
	}

	rows := batch
	if len(w.pending) > 0 {
		rows = append(w.pending, batch...)
	}
	if len(rows) == 0 {
		return
	}

	// Write the batch to local disk before the insert. If the process dies
	// between here and the marker write, the next start replays it.
	var (
		spoolName   string
		spoolOffset int64
		spoolErr    error
	)
	// Normally only the new batch goes to disk: pending is already there from
	// the run that failed to insert it. After a failed append that is no longer
	// true — pending never reached the disk — so the whole set has to be
	// written or a restart would drop those rows for good.
	spoolRows := batch
	if w.spoolDirty {
		spoolRows = rows
	}
	if len(spoolRows) > 0 {
		payload, err := encodeLogBatch(spoolRows)
		if err != nil {
			common.SysError(common.LogText("log writer encode failed: %s", err.Error()))
			w.spoolDirty = true
		} else {
			spoolName, spoolOffset, spoolErr = w.spool.Append(payload)
			if spoolErr != nil {
				common.SysError(common.LogText("log spool append failed: %s", spoolErr.Error()))
			}
			w.spoolDirty = spoolErr != nil
		}
	}

	// The insert runs on its own goroutine behind a hard deadline. Insert
	// accepts a context, but the native batch only honours it around
	// PrepareBatch, so a wedged connection used to block this loop forever. The
	// visible symptom was not a crash: the queue filled up, every later row fell
	// back to a synchronous insert, and the log page went quiet for the rows
	// that only the writer would have stored.
	if !w.busy.CompareAndSwap(false, true) {
		// The previous batch has still not come back. Hold these rows and retry
		// next round rather than starting a second insert.
		w.failed.Add(1)
		w.pending = rows
		w.stalled++
		if w.stalled >= logWriterStallRounds {
			common.SysError(common.LogText("log writer insert stalled for %d rounds; rebuilding the log database connection", w.stalled))
			w.resetInserter()
		}
		w.trimPending()
		return
	}
	w.stalled = 0

	ctx, cancel := context.WithTimeout(context.Background(), logWriterFlushTimeout)
	defer cancel()

	gen := w.gen.Load()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if w.gen.Load() == gen {
				w.busy.Store(false)
			}
		}()
		done <- w.currentInserter().Insert(ctx, rows)
	}()

	select {
	case err := <-done:
		if err != nil {
			w.failed.Add(1)
			w.pending = rows
			w.trimPending()
			return
		}
	case <-ctx.Done():
		// Stop waiting. The batch is already in the spool, so a restart replays
		// it; what matters here is that the flush loop keeps draining the queue.
		w.failed.Add(1)
		w.pending = rows
		w.stalled++
		if w.stalled >= logWriterStallRounds {
			common.SysError(common.LogText("log writer insert timed out %d times; rebuilding the log database connection", w.stalled))
			w.resetInserter()
		}
		w.trimPending()
		return
	}

	w.pending = nil
	w.written.Add(int64(len(rows)))
	if spoolErr == nil && spoolName != "" {
		if err := w.spool.Confirm(spoolName, spoolOffset); err != nil {
			common.SysError(common.LogText("log spool confirm failed: %s", err.Error()))
		}
	}
}

// replay pushes rows left in the spool by a previous process into the log
// database and confirms them. It runs before the writer goroutine starts, so
// it never races with a flush.
func (w *logWriter) replay() (int64, error) {
	var (
		batch    []*Log
		replayed int64
	)

	push := func() error {
		if len(batch) == 0 {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), logWriterFlushTimeout)
		defer cancel()
		if err := w.inserter.Insert(ctx, batch); err != nil {
			return err
		}
		replayed += int64(len(batch))
		batch = batch[:0]
		return nil
	}

	lastFile, lastOffset, err := w.spool.ForEachPending(func(line []byte) error {
		var log Log
		if err := common.Unmarshal(line, &log); err != nil {
			// A corrupt record must not block replay of the rows after it.
			common.SysError(common.LogText("log spool record skipped: %s", err.Error()))
			return nil
		}
		batch = append(batch, &log)
		if len(batch) >= w.batchMax {
			return push()
		}
		return nil
	})
	if err != nil {
		return replayed, err
	}
	if err := push(); err != nil {
		return replayed, err
	}
	w.replayed.Add(replayed)
	if lastFile != "" {
		if err := w.spool.Confirm(lastFile, lastOffset); err != nil {
			return replayed, err
		}
	}
	return replayed, nil
}

func encodeLogBatch(batch []*Log) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(len(batch) * 512)
	for _, log := range batch {
		data, err := common.Marshal(log)
		if err != nil {
			return nil, err
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// trimPending caps the in-memory retry buffer. The rows are also on disk in
// the spool, so dropping them here bounds memory without losing data.
func (w *logWriter) trimPending() {
	if len(w.pending) <= w.batchMax*10 {
		return
	}
	common.SysError(common.LogText("log writer dropping %d buffered rows; they remain in the spool for replay", len(w.pending)))
	w.pending = nil
}

func (w *logWriter) currentInserter() logInserter {
	w.inserterMu.Lock()
	defer w.inserterMu.Unlock()
	return w.inserter
}

// resetInserter abandons the current connection and opens a new one.
//
// Closing a wedged connection can itself take time, so the work runs off the
// flush goroutine: the whole point of the reset is that the flush loop keeps
// moving. gen is bumped first so the insert still parked on the old connection
// cannot clear the token the next round takes.
func (w *logWriter) resetInserter() {
	w.stalled = 0
	w.gen.Add(1)
	w.busy.Store(false)

	old := w.currentInserter()
	go func() {
		if old != nil {
			_ = old.Close()
		}
		if w.dsn == "" {
			return
		}
		next, err := newClickHouseLogInserter(w.dsn)
		if err != nil {
			common.SysError(common.LogText("log writer reconnect failed: %s", err.Error()))
			return
		}
		w.inserterMu.Lock()
		w.inserter = next
		w.inserterMu.Unlock()
		common.SysLog(common.LogText("log writer reconnected to the log database"))
	}()
}

// heartbeat prints the writer counters on a fixed cadence even when nothing is
// flowing, so "idle" and "broken" stop looking the same in the container log.
func (w *logWriter) heartbeat() {
	stats := w.stats()
	common.SysLog(common.LogText(
		"log writer alive: queued=%d pending=%d written=%d replayed=%d failed=%d fallback=%d",
		len(w.ch), len(w.pending), stats.Written, stats.Replayed, stats.Failed, stats.Dropped))
}

// clickHouseLogInserter writes a batch with the native ClickHouse protocol.
// async_insert is turned off for these writes: the application already sends
// whole batches, and letting the server buffer them again would add a second
// wait whose length is set by the DSN.
type clickHouseLogInserter struct {
	conn     clickhouse.Conn
	database string
}

func newClickHouseLogInserter(dsn string) (*clickHouseLogInserter, error) {
	opts, err := clickhouse.ParseDSN(normalizeClickHouseDSN(dsn))
	if err != nil {
		return nil, err
	}
	opts.MaxOpenConns = 2
	opts.MaxIdleConns = 2
	opts.ConnMaxLifetime = 30 * time.Minute
	// ReadTimeout defaults to five minutes. A batch that cannot be acknowledged
	// inside the flush budget is better abandoned - the spool still holds it -
	// than held open, because the writer cannot tell "slow" from "wedged".
	opts.ReadTimeout = logWriterFlushTimeout
	opts.DialTimeout = 10 * time.Second
	if opts.Settings == nil {
		opts.Settings = clickhouse.Settings{}
	}
	opts.Settings["async_insert"] = 0

	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, err
	}
	common.SysLog(common.LogText("log writer target: %s database=%s table=logs", opts.Addr, opts.Auth.Database))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &clickHouseLogInserter{conn: conn, database: opts.Auth.Database}, nil
}

func (i *clickHouseLogInserter) Insert(ctx context.Context, rows []*Log) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := i.conn.PrepareBatch(ctx, clickHouseLogInsertStatement)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := batch.Append(
			int64(row.Id),
			int32(row.UserId),
			row.CreatedAt,
			int32(row.Type),
			row.Content,
			row.Username,
			row.TokenName,
			row.ModelName,
			int32(row.Quota),
			int32(row.PromptTokens),
			int32(row.CompletionTokens),
			int32(row.UseTime),
			boolToUInt8(row.IsStream),
			int32(row.ChannelId),
			int32(row.TokenId),
			row.Group,
			row.Ip,
			row.RequestId,
			row.UpstreamRequestId,
			row.Other,
		); err != nil {
			_ = batch.Abort()
			return err
		}
	}
	return batch.Send()
}

func (i *clickHouseLogInserter) Close() error {
	return i.conn.Close()
}

func boolToUInt8(value bool) uint8 {
	if value {
		return 1
	}
	return 0
}

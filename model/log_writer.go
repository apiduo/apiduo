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
	logWriterFlushTimeout    = 60 * time.Second
	// How long a caller waits for queue space before falling back to the
	// synchronous insert path.
	logWriterDefaultEnqueueWait = 1000 // milliseconds

	clickHouseLogInsertStatement = "INSERT INTO logs (" +
		"id, user_id, created_at, type, content, username, token_name, model_name, " +
		"quota, prompt_tokens, completion_tokens, use_time, is_stream, channel_id, token_id, " +
		"`group`, ip, request_id, upstream_request_id, other)"
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
	inserter logInserter

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

	for {
		select {
		case <-ticker.C:
			w.flush(false)
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

	ctx, cancel := context.WithTimeout(context.Background(), logWriterFlushTimeout)
	defer cancel()
	if err := w.inserter.Insert(ctx, rows); err != nil {
		w.failed.Add(1)
		w.pending = rows
		// The spool still holds these rows; dropping the in-memory retry buffer
		// only bounds memory and lets the next start replay them.
		if len(w.pending) > w.batchMax*10 {
			common.SysError(common.LogText("log writer dropping %d buffered rows; they remain in the spool for replay", len(w.pending)))
			w.pending = nil
		}
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

// clickHouseLogInserter writes a batch with the native ClickHouse protocol.
// async_insert is turned off for these writes: the application already sends
// whole batches, and letting the server buffer them again would add a second
// wait whose length is set by the DSN.
type clickHouseLogInserter struct {
	conn clickhouse.Conn
}

func newClickHouseLogInserter(dsn string) (*clickHouseLogInserter, error) {
	opts, err := clickhouse.ParseDSN(normalizeClickHouseDSN(dsn))
	if err != nil {
		return nil, err
	}
	opts.MaxOpenConns = 2
	opts.MaxIdleConns = 2
	opts.ConnMaxLifetime = 30 * time.Minute
	if opts.Settings == nil {
		opts.Settings = clickhouse.Settings{}
	}
	opts.Settings["async_insert"] = 0

	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &clickHouseLogInserter{conn: conn}, nil
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

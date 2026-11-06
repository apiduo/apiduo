package model

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// gormLogInserter stores batches through GORM so the writer can be tested
// without a ClickHouse instance. Production uses clickHouseLogInserter.
type gormLogInserter struct {
	db *gorm.DB
}

func (i gormLogInserter) Insert(_ context.Context, rows []*Log) error {
	if len(rows) == 0 {
		return nil
	}
	return i.db.CreateInBatches(rows, len(rows)).Error
}

func (i gormLogInserter) Close() error { return nil }

func setupLogWriterTestDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Log{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)

	previous := LOG_DB
	LOG_DB = db
	t.Cleanup(func() {
		LOG_DB = previous
		assert.NoError(t, sqlDB.Close())
	})
}

func countStoredLogs(t *testing.T) int64 {
	t.Helper()
	var count int64
	require.NoError(t, LOG_DB.Model(&Log{}).Count(&count).Error)
	return count
}

func newTestLogWriter(t *testing.T, dir string, batchMax int) *logWriter {
	t.Helper()
	spool, err := newLogSpool(dir, 1<<20)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, spool.Close()) })
	return &logWriter{
		ch:       make(chan *Log, 256),
		interval: time.Hour,
		batchMax: batchMax,
		spool:    spool,
		inserter: gormLogInserter{db: LOG_DB},
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

func TestLogSpoolReplaysUnconfirmedRecordsOnly(t *testing.T) {
	spool, err := newLogSpool(t.TempDir(), 1<<20)
	require.NoError(t, err)

	name, offset, err := spool.Append([]byte("first\nsecond\n"))
	require.NoError(t, err)
	require.NotEmpty(t, name)
	require.Equal(t, int64(len("first\nsecond\n")), offset)

	var lines []string
	lastFile, lastOffset, err := spool.ForEachPending(func(line []byte) error {
		lines = append(lines, string(line))
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, lines)
	require.Equal(t, name, lastFile)
	require.Equal(t, offset, lastOffset)

	require.NoError(t, spool.Confirm(lastFile, lastOffset))

	lines = nil
	_, _, err = spool.ForEachPending(func(line []byte) error {
		lines = append(lines, string(line))
		return nil
	})
	require.NoError(t, err)
	assert.Empty(t, lines, "confirmed records must not be replayed")
}

func TestLogSpoolDropsConfirmedFilesAfterRotation(t *testing.T) {
	dir := t.TempDir()
	spool, err := newLogSpool(dir, 8)
	require.NoError(t, err)

	firstName, _, err := spool.Append([]byte("aaaa\n"))
	require.NoError(t, err)
	secondName, secondOffset, err := spool.Append([]byte("bbbb\n"))
	require.NoError(t, err)
	_, _, err = spool.Append([]byte("cccc\n"))
	require.NoError(t, err)

	require.NotEqual(t, firstName, secondName)
	require.Greater(t, secondName, firstName, "file names must sort in creation order")

	require.NoError(t, spool.Confirm(secondName, secondOffset))

	files, err := spool.listFilesLocked()
	require.NoError(t, err)
	assert.NotContains(t, files, firstName, "fully confirmed files must be removed")
	assert.Contains(t, files, secondName)
}

func TestLogWriterFlushStoresQueuedRows(t *testing.T) {
	setupLogWriterTestDB(t)
	writer := newTestLogWriter(t, t.TempDir(), 50)

	for i := range 25 {
		writer.ch <- &Log{UserId: i + 1, CreatedAt: int64(i), Type: LogTypeConsume}
	}
	writer.flush(false)

	assert.EqualValues(t, 25, countStoredLogs(t))
	assert.EqualValues(t, 25, writer.stats().Written)
	assert.Empty(t, writer.pending)
}

func TestLogWriterFlushKeepsFailedBatchForRetry(t *testing.T) {
	setupLogWriterTestDB(t)
	writer := newTestLogWriter(t, t.TempDir(), 50)
	require.NoError(t, LOG_DB.Migrator().DropTable(&Log{}))

	writer.ch <- &Log{UserId: 1, CreatedAt: 1, Type: LogTypeConsume}
	writer.flush(false)

	assert.EqualValues(t, 1, writer.stats().Failed)
	require.Len(t, writer.pending, 1, "a failed batch must stay for the next flush")
	assert.NotEmpty(t, writer.pending)
}

func TestLogWriterDrainsQueueOnShutdown(t *testing.T) {
	setupLogWriterTestDB(t)
	writer := newTestLogWriter(t, t.TempDir(), 7)

	go writer.run()
	for i := range 30 {
		writer.ch <- &Log{UserId: i + 1, CreatedAt: int64(i), Type: LogTypeConsume}
	}
	close(writer.stop)
	<-writer.done

	assert.EqualValues(t, 30, countStoredLogs(t), "shutdown must not drop queued rows")
}

func TestLogWriterReplaysSpoolLeftByPreviousProcess(t *testing.T) {
	setupLogWriterTestDB(t)
	dir := t.TempDir()

	previous, err := newLogSpool(dir, 1<<20)
	require.NoError(t, err)
	payload, err := encodeLogBatch([]*Log{
		{UserId: 7, CreatedAt: 100, Type: LogTypeConsume, RequestId: "r-1"},
		{UserId: 8, CreatedAt: 200, Type: LogTypeConsume, RequestId: "r-2"},
	})
	require.NoError(t, err)
	_, _, err = previous.Append(payload)
	require.NoError(t, err)
	require.NoError(t, previous.Close())

	writer := newTestLogWriter(t, dir, 10)
	replayed, err := writer.replay()
	require.NoError(t, err)
	assert.EqualValues(t, 2, replayed)
	assert.EqualValues(t, 2, countStoredLogs(t))

	// A second replay must not duplicate: the position was confirmed.
	replayed, err = writer.replay()
	require.NoError(t, err)
	assert.Zero(t, replayed)
	assert.EqualValues(t, 2, countStoredLogs(t))
}

func TestEnqueueLogFallsBackWhenWriterInactive(t *testing.T) {
	setupLogWriterTestDB(t)
	previous := apiduoLogWriter
	apiduoLogWriter = nil
	t.Cleanup(func() { apiduoLogWriter = previous })

	handled, err := enqueueLog(&Log{UserId: 1, CreatedAt: 1, Type: LogTypeConsume})
	assert.False(t, handled, "caller must insert synchronously when the writer is off")
	assert.NoError(t, err)
}

func TestEncodeLogBatchRoundTrip(t *testing.T) {
	payload, err := encodeLogBatch([]*Log{
		{UserId: 3, CreatedAt: 9, Type: LogTypeConsume, Other: `{"cache_tokens":12}`},
	})
	require.NoError(t, err)
	require.Equal(t, byte('\n'), payload[len(payload)-1])

	var decoded Log
	require.NoError(t, common.Unmarshal(payload[:len(payload)-1], &decoded))
	assert.Equal(t, 3, decoded.UserId)
	assert.Equal(t, `{"cache_tokens":12}`, decoded.Other)
}

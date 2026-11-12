package model

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blockedInserter parks every insert until release is closed. It models the
// production failure this guards against: a half-open connection where the
// native batch never observes the context it was handed.
type blockedInserter struct {
	entered   chan struct{}
	release   chan struct{}
	closed    atomic.Int32
	inFlight  atomic.Int32
	peakInFly atomic.Int32
}

func newBlockedInserter() *blockedInserter {
	return &blockedInserter{
		entered: make(chan struct{}, 64),
		release: make(chan struct{}),
	}
}

func (i *blockedInserter) Insert(_ context.Context, _ []*Log) error {
	current := i.inFlight.Add(1)
	defer i.inFlight.Add(-1)
	for {
		peak := i.peakInFly.Load()
		if current <= peak || i.peakInFly.CompareAndSwap(peak, current) {
			break
		}
	}
	i.entered <- struct{}{}
	<-i.release
	return nil
}

func (i *blockedInserter) Close() error {
	i.closed.Add(1)
	return nil
}

func newStallTestWriter(t *testing.T, inserter logInserter) *logWriter {
	t.Helper()
	spool, err := newLogSpool(t.TempDir(), 1<<20)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, spool.Close()) })
	return &logWriter{
		ch:       make(chan *Log, 16),
		interval: time.Hour,
		batchMax: 8,
		spool:    spool,
		inserter: inserter,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// A wedged insert must not hold the flush loop. Before this guard the loop
// blocked on Insert forever, the queue filled up, and every later log row fell
// back to a synchronous insert while the spool stopped growing.
func TestLogWriterFlushReturnsWhenInsertHangs(t *testing.T) {
	previous := logWriterFlushTimeout
	logWriterFlushTimeout = 50 * time.Millisecond
	t.Cleanup(func() { logWriterFlushTimeout = previous })

	inserter := newBlockedInserter()
	writer := newStallTestWriter(t, inserter)
	writer.ch <- &Log{RequestId: "hang-1"}

	started := time.Now()
	writer.flush(false)
	elapsed := time.Since(started)

	require.Less(t, elapsed, 2*time.Second, "flush blocked on a wedged insert")
	require.Len(t, writer.pending, 1, "the batch must stay pending for retry")

	close(inserter.release)
}

// The insert goroutine must stay single. A second flush while the first is
// still parked has to be skipped, not start a competing insert.
func TestLogWriterDoesNotStackHungInserts(t *testing.T) {
	previous := logWriterFlushTimeout
	logWriterFlushTimeout = 50 * time.Millisecond
	t.Cleanup(func() { logWriterFlushTimeout = previous })

	inserter := newBlockedInserter()
	writer := newStallTestWriter(t, inserter)
	writer.ch <- &Log{RequestId: "stack-1"}

	writer.flush(false)
	require.Len(t, writer.pending, 1)

	writer.ch <- &Log{RequestId: "stack-2"}
	writer.flush(false)

	require.LessOrEqual(t, inserter.peakInFly.Load(), int32(1), "flush started a second insert while one was still in flight")
	require.Len(t, writer.pending, 2, "rows must accumulate instead of being dropped")

	close(inserter.release)
}

// Enough consecutive stalls must escalate to a reconnect: without it the writer
// would keep retrying the same dead connection for the life of the process.
func TestLogWriterRebuildsConnectionAfterRepeatedStalls(t *testing.T) {
	previousTimeout, previousRounds := logWriterFlushTimeout, logWriterStallRounds
	logWriterFlushTimeout = 20 * time.Millisecond
	logWriterStallRounds = 2
	t.Cleanup(func() {
		logWriterFlushTimeout = previousTimeout
		logWriterStallRounds = previousRounds
	})

	inserter := newBlockedInserter()
	writer := newStallTestWriter(t, inserter)

	for round := range 3 {
		writer.ch <- &Log{RequestId: "stall-" + string(rune('a'+round))}
		writer.flush(false)
	}

	require.Eventually(t, func() bool {
		return inserter.closed.Load() > 0
	}, 2*time.Second, 10*time.Millisecond, "the writer never closed the wedged connection")

	close(inserter.release)
}

// The heartbeat is the only outward sign that a quiet writer is alive; it must
// not panic on a writer that never started a batch.
func TestLogWriterHeartbeatOnIdleWriter(t *testing.T) {
	writer := newStallTestWriter(t, newBlockedInserter())
	assert.NotPanics(t, writer.heartbeat)
}

package trace

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Exporter owns the two bounded queues and the flush worker. Meta and
// content snapshots travel on separate channels (docs §3.9): a burst of
// large content snapshots must never crowd out cheap metadata spans, or a
// low-traffic site loses even its metadata traces.
//
// Full queue = drop, counted, warned. No spool, no disk, no synchronous
// fallback — tracing data is expendable by design.

const (
	maxBatchSpans   = 200
	maxBatchBytes   = 2 * 1024 * 1024
	dropWarnEvery   = time.Minute
	maxBatchRetries = 1
)

type exporter struct {
	queueMeta    chan *Snapshot
	queueContent chan *Snapshot

	droppedMeta    atomic.Uint64
	droppedContent atomic.Uint64
	lastDropWarn   atomic.Int64

	startOnce sync.Once
	client    *http.Client
}

var (
	exporterMu   sync.Mutex
	currentExp   *exporter
	defaultsOnce sync.Once
)

// exporterFor returns the exporter for the current config, building it (and
// its queues) on first use. Tests swap configs via setConfig, which resets
// the exporter.
func exporterFor(conf *Config) *exporter {
	exporterMu.Lock()
	defer exporterMu.Unlock()
	if currentExp == nil || cap(currentExp.queueMeta) != conf.QueueMeta || cap(currentExp.queueContent) != conf.QueueContent {
		currentExp = &exporter{
			queueMeta:    make(chan *Snapshot, conf.QueueMeta),
			queueContent: make(chan *Snapshot, conf.QueueContent),
			client: &http.Client{
				Timeout: time.Duration(conf.TimeoutSeconds) * time.Second,
			},
		}
	}
	return currentExp
}

// resetExporter drops the current exporter so a config change rebuilds the
// queues. In-flight worker goroutines drain their old channels and exit
// when they see them closed-or-empty at shutdown; losing the in-flight
// batch on a config swap is acceptable (traces are expendable).
func resetExporter(conf *Config) {
	exporterMu.Lock()
	currentExp = nil
	exporterMu.Unlock()
}

func (e *exporter) enqueue(sn *Snapshot) {
	conf := current()
	target := e.queueMeta
	isContent := sn.HasContent && conf.BodyMode != BodyOff
	if isContent {
		target = e.queueContent
	}
	select {
	case target <- sn:
	default:
		if isContent {
			e.droppedContent.Add(1)
		} else {
			e.droppedMeta.Add(1)
		}
		e.maybeWarnDrops()
	}
	e.startOnce.Do(func() { go e.worker(conf) })
}

func (e *exporter) maybeWarnDrops() {
	now := time.Now().Unix()
	last := e.lastDropWarn.Load()
	if now-last < int64(dropWarnEvery/time.Second) {
		return
	}
	if e.lastDropWarn.CompareAndSwap(last, now) {
		log.Printf("[apiduo-trace] dropped snapshots: meta=%d content=%d (queues full; traces are expendable by design)",
			e.droppedMeta.Load(), e.droppedContent.Load())
	}
}

func (e *exporter) worker(conf *Config) {
	interval := time.Duration(conf.FlushIntervalMS) * time.Millisecond
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var batch []*Snapshot
	var batchBytes int
	ticked := false
	for {
		select {
		case sn := <-e.queueMeta:
			batch, batchBytes = appendBatch(batch, sn, batchBytes)
		case sn := <-e.queueContent:
			batch, batchBytes = appendBatch(batch, sn, batchBytes)
		case <-ticker.C:
			ticked = true
		}
		// Drain whatever else is immediately available, meta first so a
		// backlog of content cannot starve metadata spans.
	drain:
		for len(batch) < maxBatchSpans && batchBytes < maxBatchBytes {
			select {
			case sn := <-e.queueMeta:
				batch, batchBytes = appendBatch(batch, sn, batchBytes)
			case sn := <-e.queueContent:
				batch, batchBytes = appendBatch(batch, sn, batchBytes)
			default:
				break drain
			}
		}
		if len(batch) == 0 {
			ticked = false
			continue
		}
		// Flush when the batch is full, when the interval elapsed, or when
		// the queues are running deep. A small batch must flush on the
		// tick — otherwise a lone span would wait forever.
		if ticked || len(batch) >= maxBatchSpans || batchBytes >= maxBatchBytes ||
			len(e.queueMeta)+len(e.queueContent) > maxBatchSpans/2 {
			e.flush(conf, batch)
			batch, batchBytes = nil, 0
			ticked = false
		}
	}
}

func appendBatch(batch []*Snapshot, sn *Snapshot, bytesSoFar int) ([]*Snapshot, int) {
	return append(batch, sn), bytesSoFar + snapshotByteSize(sn)
}

func snapshotByteSize(sn *Snapshot) int {
	return len(sn.Input) + len(sn.Output) + 512
}

func (e *exporter) flush(route *Config, batch []*Snapshot) {
	// Two configs, two lifetimes:
	//
	// Routing (host, credentials) belongs to the config this worker was
	// started with. Those only change with a restart, and a stale worker
	// must never be re-aimed at a different endpoint mid-flight — tests
	// swap sinks, and an operator's canary host must not receive batches
	// queued for the old one.
	//
	// Per-export decisions (redaction, environment tag, release) are read
	// live, because the console edits them and an operator who turns
	// redaction ON must not have to restart the process for it to take
	// effect — a compliance switch that needs a deploy is a compliance
	// switch that will be wrong for a while.
	body := BuildExportBodyWithMedia(liveExportConfig(route), batch)
	if err := e.post(route, body); err != nil {
		log.Printf("[apiduo-trace] export failed (%d spans dropped after retry): %v", len(batch), err)
	}
}

// liveExportConfig copies the worker's routing config and overlays the
// per-export fields from the live one. nil live config (process shutting
// down) keeps the captured values.
func liveExportConfig(route *Config) *Config {
	merged := *route
	if cur := current(); cur != nil {
		merged.Redact = cur.Redact
		merged.Environment = cur.Environment
		merged.Release = cur.Release
	}
	return &merged
}

func (e *exporter) post(conf *Config, body []byte) error {
	endpoint := strings.TrimSuffix(conf.Host, "/") + "/api/public/otel/v1/traces"
	auth := base64.StdEncoding.EncodeToString([]byte(conf.PublicKey + ":" + conf.SecretKey))
	var lastErr error
	for attempt := range maxBatchRetries + 1 {
		if attempt > 0 {
			time.Sleep(time.Second)
		}
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Basic "+auth)
		req.Header.Set("x-langfuse-ingestion-version", "4")
		resp, err := e.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		// 4xx (except 429) will fail identically on retry; drop the batch.
		if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return fmt.Errorf("langfuse rejected batch: HTTP %d", resp.StatusCode)
		}
		lastErr = fmt.Errorf("langfuse HTTP %d", resp.StatusCode)
	}
	return lastErr
}

// DroppedCounts exposes the drop counters for tests and diagnostics.
func DroppedCounts() (meta, content uint64) {
	exporterMu.Lock()
	e := currentExp
	exporterMu.Unlock()
	if e == nil {
		return 0, 0
	}
	return e.droppedMeta.Load(), e.droppedContent.Load()
}

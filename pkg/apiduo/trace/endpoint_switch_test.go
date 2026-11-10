package trace

import (
	"testing"
	"time"
)

// The export endpoint used to be environment-only, on the theory that a
// settings row must never be able to redirect spans. The console now edits it,
// so two things have to hold: the running config follows the save, and the
// NEXT batch goes to the new host — not to the one the old worker captured.
func TestEndpointSwitchMovesExports(t *testing.T) {
	oldSink := newSink(t)
	nextSink := newSink(t)

	conf := testConfig(oldSink.server.URL)
	conf.FlushIntervalMS = 20
	setConfig(conf)

	emit := func(requestID string) {
		now := time.Now()
		exporterFor(current()).enqueue(&Snapshot{
			TraceID:        TraceID(requestID),
			SpanID:         SpanID(requestID),
			RequestID:      requestID,
			Name:           "chat gpt-4o-mini",
			Model:          "gpt-4o-mini",
			Success:        true,
			ContentSampled: true,
			Input:          []byte(`{"messages":[]}`),
			StartUnixNano:  now.Add(-time.Second).UnixNano(),
			EndUnixNano:    now.UnixNano(),
			LatencyMs:      1000,
		})
	}

	emit("before-switch")
	oldSink.waitSpans(t, 1, 3*time.Second)
	before := oldSink.requestCount()

	ApplySettings(Settings{
		Defaults:    Rates{Metadata: 100, Content: 100},
		ParentBased: true,
		Endpoint:    &EndpointSettings{Host: nextSink.server.URL},
	})

	cfg := CurrentConfig()
	if cfg.Host != nextSink.server.URL {
		t.Fatalf("running export host: %q", cfg.Host)
	}
	// Empty ConsoleURL means "same as Host", exactly as it does in the
	// environment — the jump link must not keep pointing at the old instance.
	if got := cfg.ConsoleBase(); got != nextSink.server.URL {
		t.Fatalf("console base must fall back to the new host: %q", got)
	}

	emit("after-switch")
	nextSink.waitSpans(t, 1, 3*time.Second)

	// Give the old worker a beat to flush anything it still held. It must not
	// receive the post-switch span: a stale worker re-aimed mid-flight is how
	// a canary batch ends up in production.
	time.Sleep(120 * time.Millisecond)
	if got := oldSink.requestCount(); got != before {
		t.Fatalf("old sink received %d more request(s) after the switch", got-before)
	}
}

// An unrelated save must not reset the exporter: every reset leaks an idle
// worker goroutine, and the rates are saved far more often than the endpoint.
func TestUnrelatedSaveKeepsTheExporter(t *testing.T) {
	s := newSink(t)
	conf := testConfig(s.server.URL)
	conf.FlushIntervalMS = 20
	setConfig(conf)

	first := exporterFor(current())

	ApplySettings(Settings{
		Defaults:    Rates{Metadata: 50, Content: 10},
		ParentBased: true,
		Quality:     &QualitySettings{ErrorSampleRate: 100, Redact: true, Environment: "staging"},
	})

	if again := exporterFor(current()); again != first {
		t.Fatal("a sampling-only save must not rebuild the exporter")
	}
	if got := CurrentConfig().Environment; got != "staging" {
		t.Fatalf("quality settings must still apply: %q", got)
	}
}

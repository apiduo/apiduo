package trace

import (
	"encoding/hex"
	"testing"
)

func TestTraceIDStable(t *testing.T) {
	// The log page derives the Langfuse URL from the request id, and the
	// exporter derives the span's trace id the same way. If this mapping
	// ever drifts, the two sides disagree and every link 404s — so the
	// exact bytes are pinned.
	got := TraceID("2026110812000000000000a1b2c3d4e5f6")
	want := "d60b7f3c843d6a7b9e3a12bf6a942c4d"
	if got != want {
		t.Fatalf("TraceID changed: got %s want %s", got, want)
	}
	if len(got) != 32 {
		t.Fatalf("trace id must be 32 hex chars, got %d", len(got))
	}
	if TraceID("") != "" {
		t.Fatal("empty request id must map to empty trace id")
	}
	// Determinism.
	if TraceID("req-1") != TraceID("req-1") {
		t.Fatal("TraceID must be deterministic")
	}
	if TraceID("req-1") == TraceID("req-2") {
		t.Fatal("distinct request ids must map to distinct trace ids")
	}
}

func TestSpanID(t *testing.T) {
	req := "2026110812000000000000a1b2c3d4e5f6"
	span := SpanID(req)
	if len(span) != 16 {
		t.Fatalf("span id must be 16 hex chars, got %q", span)
	}
	if _, err := hex.DecodeString(span); err != nil {
		t.Fatalf("span id must be hex: %v", err)
	}
	// Different bytes than the trace id's slice.
	if span == TraceID(req)[16:32] {
		t.Fatal("span id should use different digest bytes than the trace id tail")
	}
}

func TestParseTraceparent(t *testing.T) {
	// W3C reference vector.
	tp, ok := ParseTraceparent("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	if !ok {
		t.Fatal("valid traceparent must parse")
	}
	if tp.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace id: %s", tp.TraceID)
	}
	if tp.ParentSpanID != "00f067aa0ba902b7" {
		t.Fatalf("parent span id: %s", tp.ParentSpanID)
	}
	if !tp.Sampled {
		t.Fatal("flags 01 means sampled")
	}

	tp, ok = ParseTraceparent("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00")
	if !ok || tp.Sampled {
		t.Fatal("flags 00 means not sampled")
	}

	for _, bad := range []string{
		"",
		"garbage",
		"01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", // unsupported version
		"00-short-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-short-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-x1",
	} {
		if _, ok := ParseTraceparent(bad); ok {
			t.Fatalf("must reject %q", bad)
		}
	}
}

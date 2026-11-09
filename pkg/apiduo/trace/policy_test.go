package trace

import (
	"net/http"
	"testing"
	"time"
)

func at(t *testing.T, day time.Weekday, hour, minute int) time.Time {
	t.Helper()
	// 2026-11-08 is a Sunday, so adding the weekday offset gives a stable date
	// for every case.
	base := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	return base.AddDate(0, 0, int(day)).Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
}

// First match wins, and an exclusion above a rate rule must shadow it — the
// order is the whole API contract of the policy, so it gets its own test.
func TestPolicyFirstMatchWins(t *testing.T) {
	policy := &Policy{
		Defaults: Rates{Metadata: 100, Content: 100},
		Rules: []Rule{
			{Models: []string{"blocked"}, Exclude: true},
			{Models: []string{"cheap"}, Rates: Rates{Metadata: 20, Content: 0}},
		},
	}
	policy.Rules[0].compile()
	policy.Rules[1].compile()

	now := at(t, time.Monday, 12, 0)
	if _, excluded := policy.Resolve("blocked", now); !excluded {
		t.Fatal("excluded model must be excluded")
	}
	rates, excluded := policy.Resolve("cheap", now)
	if excluded || rates.Metadata != 20 || rates.Content != 0 {
		t.Fatalf("model rule not applied: %+v excluded=%v", rates, excluded)
	}
	rates, excluded = policy.Resolve("other", now)
	if excluded || rates.Metadata != 100 || rates.Content != 100 {
		t.Fatalf("defaults not applied: %+v excluded=%v", rates, excluded)
	}
	// Case-insensitive on purpose: upstream naming is not consistent.
	rates, _ = policy.Resolve("CHEAP", now)
	if rates.Metadata != 20 {
		t.Fatalf("model match must be case-insensitive, got %+v", rates)
	}
}

// A window 22:00–06:00 wraps midnight; 06:00 exactly is outside because the
// end is exclusive (a rule ending at 18:00 must not also cover 18:00:00).
func TestPolicyWindowWrapAndBoundaries(t *testing.T) {
	policy := &Policy{
		Defaults: Rates{Metadata: 100, Content: 100},
		Rules: []Rule{
			{HasWindow: true, StartMinute: 22 * 60, EndMinute: 6 * 60, Rates: Rates{Metadata: 5, Content: 5}},
			{HasWindow: true, StartMinute: 9 * 60, EndMinute: 18 * 60, Rates: Rates{Metadata: 50, Content: 50}},
		},
	}
	policy.Rules[0].compile()
	policy.Rules[1].compile()

	cases := []struct {
		hour, minute, want int
	}{
		{22, 0, 5}, {23, 59, 5}, {0, 0, 5}, {5, 59, 5},
		{6, 0, 100}, {12, 0, 50}, {9, 0, 50}, {17, 59, 50},
		{18, 0, 100}, {8, 59, 100},
	}
	for _, c := range cases {
		rates, _ := policy.Resolve("m", at(t, time.Wednesday, c.hour, c.minute))
		if rates.Metadata != c.want {
			t.Fatalf("%02d:%02d expected %d%%, got %d", c.hour, c.minute, c.want, rates.Metadata)
		}
	}
}

func TestPolicyWeekdayFilter(t *testing.T) {
	policy := &Policy{
		Defaults: Rates{Metadata: 0, Content: 0},
		Rules: []Rule{{
			Days:  []int{1, 2, 3, 4, 5},
			Rates: Rates{Metadata: 100, Content: 100},
		}},
	}
	policy.Rules[0].compile()

	if rates, _ := policy.Resolve("m", at(t, time.Monday, 10, 0)); rates.Metadata != 100 {
		t.Fatal("Monday must hit the weekday rule")
	}
	if rates, _ := policy.Resolve("m", at(t, time.Sunday, 10, 0)); rates.Metadata != 0 {
		t.Fatal("Sunday must fall through to the defaults")
	}
}

// A missing policy must not silently stop tracing: losing every span because a
// settings row is absent is worse than an unexpected rate.
func TestNilPolicySamplesEverything(t *testing.T) {
	var policy *Policy
	rates, excluded := policy.Resolve("m", time.Now())
	if excluded || rates.Metadata != 100 || rates.Content != 100 {
		t.Fatalf("nil policy must default to full sampling: %+v", rates)
	}
}

// Parent-based sampling: a client that already sampled keeps its span even
// when the local rate is 0, and a client that declined is respected even at
// 100 — otherwise the gateway invents children under a trace the agent
// explicitly did not sample.
func TestParentBasedSampling(t *testing.T) {
	s := newSink(t)
	conf := testConfig(s.server.URL)
	conf.FlushIntervalMS = 30
	conf.Policy = &Policy{Defaults: Rates{Metadata: 0, Content: 100}}
	setConfig(conf)

	sampled := newTestContext(t)
	sampled.Request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	Begin(sampled)
	if id := FillUsage(sampled, Usage{PromptTokens: 1}); id == "" {
		t.Fatal("parent sampled=1 must keep the trace even at local rate 0")
	}
	FinalizeRelay(sampled, RelayMeta{Model: "m", Success: true, Operation: "chat", StartUnixNano: time.Now().UnixNano()})

	conf2 := testConfig(s.server.URL)
	conf2.FlushIntervalMS = 30
	conf2.Policy = &Policy{Defaults: Rates{Metadata: 100, Content: 100}}
	setConfig(conf2)
	refused := newTestContext(t)
	refused.Request.Header.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-00")
	Begin(refused)
	if id := FillUsage(refused, Usage{PromptTokens: 1}); id != "" {
		t.Fatalf("parent sampled=0 must not produce a trace id, got %q", id)
	}
	FinalizeRelay(refused, RelayMeta{Model: "m", Success: true, Operation: "chat", StartUnixNano: time.Now().UnixNano()})

	if spans := s.waitSpans(t, 1, 3*time.Second); len(spans) != 1 {
		t.Fatalf("exactly the parent-sampled trace must be exported, got %d", len(spans))
	}
}

// Turning the parent flag off must make routing purely local: sampled=0 then
// traces normally.
func TestParentBasedOffIgnoresClientBit(t *testing.T) {
	s := newSink(t)
	conf := testConfig(s.server.URL)
	conf.FlushIntervalMS = 30
	conf.ParentBased = false
	conf.Policy = &Policy{Defaults: Rates{Metadata: 100, Content: 100}}
	setConfig(conf)

	c := newTestContext(t)
	c.Request.Header.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-00")
	Begin(c)
	if id := FillUsage(c, Usage{PromptTokens: 1}); id == "" {
		t.Fatal("with parent-based off the local rate decides")
	}
	FinalizeRelay(c, RelayMeta{Model: "m", Success: true, Operation: "chat", StartUnixNano: time.Now().UnixNano()})
	if spans := s.waitSpans(t, 1, 3*time.Second); len(spans) != 1 {
		t.Fatalf("expected one span, got %d", len(spans))
	}
}

// Content rate 0 must keep the span (so the console link still works) and drop
// only prompt/answer, with the attribute explaining why it is empty.
func TestContentRateZeroKeepsMetadataOnly(t *testing.T) {
	s := newSink(t)
	conf := testConfig(s.server.URL)
	conf.FlushIntervalMS = 30
	conf.Policy = &Policy{Defaults: Rates{Metadata: 100, Content: 0}}
	setConfig(conf)

	c := newTestContext(t)
	c.Set(ginKeyBodyStorage, stubBody{data: []byte(`{"messages":[{"role":"user","content":"secret"}]}`)})
	Begin(c)
	BindModel(c, "m")
	if id := FillUsage(c, Usage{PromptTokens: 3}); id == "" {
		t.Fatal("metadata sampling must still hand out a trace id")
	}
	FinalizeRelay(c, RelayMeta{Model: "m", Success: true, Operation: "chat", StartUnixNano: time.Now().UnixNano()})

	spans := s.waitSpans(t, 1, 3*time.Second)
	sp := spans[0]
	if hasAttr(t, sp, "langfuse.observation.input") {
		t.Fatal("content rate 0 must not ship the prompt")
	}
	if v, _ := attrValue(t, sp, "apiduo.content_sampled").(bool); v {
		t.Fatal("content_sampled must be false")
	}
}

// An excluded model must stay invisible even when the request fails, where the
// error rate would otherwise force a span.
func TestExcludedModelNeverEmitsOnFailure(t *testing.T) {
	s := newSink(t)
	conf := testConfig(s.server.URL)
	conf.FlushIntervalMS = 30
	conf.ErrorSampleRate = 100
	conf.Policy = &Policy{
		Defaults: Rates{Metadata: 100, Content: 100},
		Rules:    []Rule{{Models: []string{"blocked"}, Exclude: true}},
	}
	conf.Policy.Rules[0].compile()
	setConfig(conf)

	c := newTestContext(t)
	Begin(c)
	BindModel(c, "blocked")
	FinalizeRelay(c, RelayMeta{
		Model: "blocked", Success: false, ErrorStatus: 500,
		Operation: "chat", StartUnixNano: time.Now().UnixNano(),
	})
	time.Sleep(150 * time.Millisecond)
	if got := s.requestCount(); got != 0 {
		t.Fatalf("excluded model must never export, got %d requests", got)
	}
}

// The policy clock is read in the configured zone, not the process zone: a
// 09:00 window configured for Asia/Shanghai must not depend on the container's
// TZ (which is UTC in every container image we ship).
func TestPolicyUsesConfiguredTimezone(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	policy := &Policy{
		Defaults: Rates{Metadata: 0, Content: 0},
		Rules:    []Rule{{HasWindow: true, StartMinute: 9 * 60, EndMinute: 18 * 60, Rates: Rates{Metadata: 100, Content: 100}}},
	}
	policy.Rules[0].compile()

	// 01:00 UTC is 09:00 in Shanghai: inside the window.
	inside := time.Date(2026, 11, 9, 1, 0, 0, 0, time.UTC).In(shanghai)
	if rates, _ := policy.Resolve("m", inside); rates.Metadata != 100 {
		t.Fatal("09:00 Asia/Shanghai must be inside a 09:00-18:00 window")
	}
	// 10:00 UTC is 18:00 in Shanghai: outside (end is exclusive).
	outside := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC).In(shanghai)
	if rates, _ := policy.Resolve("m", outside); rates.Metadata != 0 {
		t.Fatal("18:00 Asia/Shanghai must be outside the window")
	}
}

// BindModel must win over the later settlement call: the first verdict is the
// one the log row and the span share.
func TestBindModelFixesVerdictOnce(t *testing.T) {
	setConfig(testConfig("http://unused"))
	c := newTestContext(t)
	Begin(c)
	BindModel(c, "first")
	BindModel(c, "second")
	col := fromContext(c)
	if col.model != "first" || !col.decided {
		t.Fatalf("first bind must win: model=%q decided=%v", col.model, col.decided)
	}
}

// A client-supplied traceparent without a sampled bit we understand still
// parses: only "01"-style flags mean sampled.
func TestTraceparentFlagParsing(t *testing.T) {
	yes := []string{"01", "03", "05", "07", "09", "0b", "0d", "0f"}
	no := []string{"00", "02", "04", "06", "08", "0a", "0c", "0e"}
	for _, flag := range yes {
		header := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-" + flag
		if p, ok := ParseTraceparent(header); !ok || !p.Sampled {
			t.Fatalf("flags %s must parse as sampled", flag)
		}
	}
	for _, flag := range no {
		header := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-" + flag
		if p, ok := ParseTraceparent(header); !ok || p.Sampled {
			t.Fatalf("flags %s must parse as not sampled", flag)
		}
	}
	_ = http.Header{}
}

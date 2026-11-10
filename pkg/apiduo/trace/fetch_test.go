package trace

import (
	"encoding/json"
	"testing"
)

func TestMapObservationWhitelist(t *testing.T) {
	lat, ttft := 2.4, 0.38
	row := rawObservation{
		ID:               "abc",
		TraceID:          "6c65e85975171df231e8bcc957efae7d",
		Type:             "GENERATION",
		Name:             "chat gpt-4o",
		Level:            "ERROR",
		StatusMessage:    "upstream 500",
		StartTime:        "2026-11-10T07:00:00.000Z",
		EndTime:          "2026-11-10T07:00:02.400Z",
		Latency:          &lat,
		TimeToFirstToken: &ttft,
		Model:            "gpt-4o",
		Input:            `{"messages":[]}`,
		Output:           "hello",
		InputUsage:       31,
		OutputUsage:      16,
		Environment:      "production",
		Release:          "v0.0.30",
		Metadata: map[string]any{
			"request_id":                           "req-1",
			"channel_id":                           "16",
			"group":                                "default",
			"token_name":                           "模型测试",
			"format":                               "openai.chat",
			"attributes.apiduo.content_sampled":    true,
			"attributes.gen_ai.usage.input_tokens": float64(31),
		},
	}

	obs := mapObservation(row)
	if obs.TraceID != row.TraceID || obs.Level != "ERROR" || obs.StatusMessage != "upstream 500" {
		t.Fatalf("identity fields lost: %+v", obs)
	}
	if obs.LatencySec != 2.4 || obs.TtftSec != 0.38 {
		t.Fatalf("timing = %v/%v", obs.LatencySec, obs.TtftSec)
	}
	if obs.PromptTokens != 31 || obs.OutputTokens != 16 {
		t.Fatalf("usage = %d/%d", obs.PromptTokens, obs.OutputTokens)
	}
	if obs.RequestID != "req-1" || obs.ChannelID != "16" || obs.Group != "default" ||
		obs.TokenName != "模型测试" || obs.Format != "openai.chat" {
		t.Fatalf("metadata dimensions lost: %+v", obs)
	}
	if !obs.ContentSampled {
		t.Fatal("content_sampled attribute must win over heuristics")
	}
}

// A span written before the flag existed has no attributes.apiduo.content_sampled;
// the fallback must not claim "not sampled" when text is present.
func TestContentSampledFallback(t *testing.T) {
	withText := mapObservation(rawObservation{Input: "hi", Output: "there"})
	if !withText.ContentSampled {
		t.Fatal("text present but reported as unsampled")
	}
	empty := mapObservation(rawObservation{Metadata: map[string]any{}})
	if empty.ContentSampled {
		t.Fatal("no text and no flag must report unsampled")
	}
	// An explicit false outranks the presence of text: the exporter wrote the
	// flag, so the flag is the source of truth.
	flagged := mapObservation(rawObservation{
		Input:    "hi",
		Metadata: map[string]any{"attributes.apiduo.content_sampled": false},
	})
	if flagged.ContentSampled {
		t.Fatal("explicit false ignored")
	}
}

func TestPickRootPrefersFlaggedRoot(t *testing.T) {
	rows := []rawObservation{
		{ID: "child", TraceID: "t"},
		{ID: "root", TraceID: "t", IsRoot: true},
	}
	if got := pickRoot(rows); got.ID != "root" {
		t.Fatalf("picked %q, want root", got.ID)
	}
	// No flag anywhere: earliest row is the closest thing to the request itself.
	if got := pickRoot([]rawObservation{{ID: "first"}, {ID: "second"}}); got.ID != "first" {
		t.Fatalf("picked %q, want first", got.ID)
	}
}

func TestAnyToTextFlattensObjects(t *testing.T) {
	var decoded any
	raw := `{"model":"x","stream":true}`
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	// Langfuse may hand back a decoded object; the drawer expects text.
	if got := anyToText(decoded); got != raw {
		t.Fatalf("object flatten = %q, want %q", got, raw)
	}
	if got := anyToText("plain"); got != "plain" {
		t.Fatalf("string passthrough = %q", got)
	}
	if got := anyToText(nil); got != "" {
		t.Fatalf("nil = %q, want empty", got)
	}
}

func TestFetchRejectsWhenDisabled(t *testing.T) {
	prev := current()
	setConfig(&Config{Enabled: false})
	defer setConfig(prev)

	if _, err := FetchObservation("6c65e85975171df231e8bcc957efae7d"); err != ErrTraceDisabled {
		t.Fatalf("err = %v, want ErrTraceDisabled", err)
	}
}

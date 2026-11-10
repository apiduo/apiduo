package trace

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestDecodeRootPrefersFlaggedRoot(t *testing.T) {
	// Real page order: startTime DESC, root somewhere in the middle.
	body := `{"data":[
		{"id":"later","startTime":"2026-11-08T12:15:43.185Z"},
		{"id":"root","startTime":"2026-11-08T11:31:07.011Z","isRootObservation":true},
		{"id":"earliest","startTime":"2026-11-08T11:00:00.000Z"}
	],"meta":{}}`
	got, err := decodeRootObservation(strings.NewReader(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "root" {
		t.Fatalf("picked %q, want root", got.ID)
	}
}

// The page arrives in startTime DESC order, so the earliest span is the *last*
// row. Picking rows[0] here would return the newest child span instead of the
// request itself.
func TestDecodeRootFallsBackToEarliestNotFirst(t *testing.T) {
	body := `{"data":[
		{"id":"latest","startTime":"2026-11-08T12:15:43.185Z"},
		{"id":"earliest","startTime":"2026-11-08T11:00:00.000Z"},
		{"id":"middle","startTime":"2026-11-08T11:30:00.000Z"}
	]}`
	got, err := decodeRootObservation(strings.NewReader(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "earliest" {
		t.Fatalf("picked %q, want earliest", got.ID)
	}
}

// Order of keys is not part of the API contract.
func TestDecodeRootToleratesMetaBeforeData(t *testing.T) {
	body := `{"meta":{"page":1},"data":[{"id":"root","isRootObservation":true}]}`
	got, err := decodeRootObservation(strings.NewReader(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "root" {
		t.Fatalf("picked %q, want root", got.ID)
	}
}

func TestDecodeRootEmptyPage(t *testing.T) {
	if _, err := decodeRootObservation(strings.NewReader(`{"data":[],"meta":{}}`)); err != ErrObservationNotFound {
		t.Fatalf("err = %v, want ErrObservationNotFound", err)
	}
	if _, err := decodeRootObservation(strings.NewReader(`{"meta":{}}`)); err != ErrObservationNotFound {
		t.Fatalf("missing data key: err = %v, want ErrObservationNotFound", err)
	}
}

// The whole point of streaming: a page far larger than the old 4 MB ceiling
// must still resolve, and must not be read to the end to do it.
func TestDecodeRootStopsAtTheRoot(t *testing.T) {
	const tail = 8 << 20
	blob := strings.Repeat("A", tail) // the b64 of a large image
	body := `{"data":[{"id":"root","isRootObservation":true,"output":"` + blob[:1024] + `"},` +
		`{"id":"child","output":"` + blob + `"}]}`

	cr := &countingReader{r: strings.NewReader(body)}
	got, err := decodeRootObservation(cr)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "root" {
		t.Fatalf("picked %q, want root", got.ID)
	}
	if cr.n > 1<<20 {
		t.Fatalf("read %d bytes of a %d-byte page; the tail was buffered", cr.n, len(body))
	}
}

// End to end through the HTTP path: the old code failed this as
// "langfuse response: unexpected end of JSON input".
func TestFetchHandlesPagesLargerThanTheOldCap(t *testing.T) {
	blob := strings.Repeat("B", 6<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"root","isRootObservation":true,"input":"hi","output":"`+blob+`"}]}`)
	}))
	defer srv.Close()

	prev := current()
	setConfig(&Config{Enabled: true, Host: srv.URL, PublicKey: "pk", SecretKey: "sk"})
	defer setConfig(prev)

	obs, err := FetchObservation("6c65e85975171df231e8bcc957efae7d")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if obs.ObservationID != "root" || len(obs.Output) != 6<<20 {
		t.Fatalf("body lost: id=%q output=%d", obs.ObservationID, len(obs.Output))
	}
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
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

package trace

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Console read path: the log page's trace drawer asks the gateway for one
// trace, and the gateway asks Langfuse. The browser never sees the secret key.
//
// This file is deliberately separate from the exporter: exporting is
// fire-and-forget and drops on pressure, reading is a synchronous user-facing
// request that must answer or fail fast.

// ErrTraceDisabled means the deployment has no working export target, so there
// is nothing to read either. Callers surface this as "not configured", not 500.
var ErrTraceDisabled = errors.New("trace export is disabled")

// ErrObservationNotFound is returned when the trace exists in the log row but
// never reached Langfuse (dropped queue, sampling side, retention).
var ErrObservationNotFound = errors.New("observation not found")

// Observation is the whitelisted slice of a Langfuse observation the console
// renders.
//
// The raw response carries 40+ fields — pricing tiers, internal model ids, the
// flattened OTel attribute dump. None of that belongs in a log drawer, and some
// of it (channel routing attributes) the log page already shows in a shape the
// operator understands. So we map explicitly instead of passing the payload
// through: a Langfuse field rename then breaks compilation here, not the UI.
type Observation struct {
	// ObservationID is the Langfuse "t-…" span id. The trace id alone opens
	// the trace list page; this is what makes the peek URL land on the exact
	// span, which is where the prompt/answer actually live.
	ObservationID string `json:"observation_id"`
	TraceID       string `json:"trace_id"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	Level         string `json:"level"`
	StatusMessage string `json:"status_message"`
	StartTime     string `json:"start_time"`
	EndTime       string `json:"end_time"`

	// Seconds. Both are zero when Langfuse has no value (non-stream requests
	// have no TTFT; a still-running span has no end).
	LatencySec float64 `json:"latency_s"`
	TtftSec    float64 `json:"ttft_s"`

	Model        string `json:"model"`
	Input        string `json:"input"`
	Output       string `json:"output"`
	PromptTokens int64  `json:"prompt_tokens"`
	OutputTokens int64  `json:"completion_tokens"`

	Environment string `json:"environment"`
	Release     string `json:"release"`

	// ContentSampled is false when metadata sampling kept the span but content
	// sampling stripped input/output. The drawer must say so explicitly —
	// an empty prompt otherwise reads as a capture bug.
	ContentSampled bool `json:"content_sampled"`

	// The dimensions ops filters on. Empty when the span predates the
	// langfuse.trace.metadata.* mapping.
	RequestID string `json:"request_id"`
	ChannelID string `json:"channel_id"`
	Group     string `json:"group"`
	TokenName string `json:"token_name"`
	Format    string `json:"format"`
}

// rawObservation mirrors only the JSON keys we read. Pointers distinguish
// "absent" from "zero" for the numeric fields, because a 0-second TTFT and a
// missing TTFT mean different things to the UI.
type rawObservation struct {
	ID               string         `json:"id"`
	TraceID          string         `json:"traceId"`
	Type             string         `json:"type"`
	Name             string         `json:"name"`
	Level            string         `json:"level"`
	StatusMessage    string         `json:"statusMessage"`
	StartTime        string         `json:"startTime"`
	EndTime          string         `json:"endTime"`
	Latency          *float64       `json:"latency"`
	TimeToFirstToken *float64       `json:"timeToFirstToken"`
	Model            string         `json:"model"`
	Input            any            `json:"input"`
	Output           any            `json:"output"`
	InputUsage       int64          `json:"inputUsage"`
	OutputUsage      int64          `json:"outputUsage"`
	Environment      string         `json:"environment"`
	Release          string         `json:"release"`
	Metadata         map[string]any `json:"metadata"`
	IsRoot           bool           `json:"isRootObservation"`
	ParentID         *string        `json:"parentObservationId"`
}

// observationFields is the minimum field set that still carries input, output,
// usage, model and metadata. Without it the v2 list endpoint returns a summary
// row only (the same fields the table shows), which is useless for a detail
// view. Order matters to Langfuse's parser, not to us.
const observationFields = "basic,io,usage,metadata,model,time,trace_context"

// fetchTimeout bounds the user-facing read. Traces are diagnostics: a slow
// Langfuse must not hold a console request open.
//
// 12s, tuned against real reads on the production Langfuse:
//   - freshly ingested span, small body:      < 1 s
//   - backfilled span (virtual root "t-<id>"): ~6.6 s on first read
//   - large body parked in blob storage:        ~2.3 s per read
// Four seconds turned the backfilled case into a 502 that looked like an
// outage; eight was fine until a body had to come back from object storage on
// a cold path. The query itself is milliseconds — this budget is for the blob
// round-trip, not for ClickHouse.
const fetchTimeout = 12 * time.Second

// FetchObservation returns the root observation of one trace.
//
// Langfuse v4 removed the single-observation endpoint (GET
// /api/public/v2/observations/{id} → 404), so the only way in is the list
// filtered by traceId. A request that arrived with a traceparent is a tree, and
// only the root carries the gateway's own prompt/answer — child spans belong to
// the calling agent. So we pick isRootObservation, falling back to the earliest
// span when the flag is missing.
func FetchObservation(traceID string) (*Observation, error) {
	conf := current()
	if !conf.Enabled || conf.Host == "" || conf.PublicKey == "" || conf.SecretKey == "" {
		return nil, ErrTraceDisabled
	}

	endpoint := strings.TrimSuffix(conf.Host, "/") + "/api/public/v2/observations"
	q := url.Values{}
	q.Set("traceId", traceID)
	q.Set("fields", observationFields)
	q.Set("limit", "20")

	req, err := http.NewRequest(http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	auth := base64.StdEncoding.EncodeToString([]byte(conf.PublicKey + ":" + conf.SecretKey))
	req.Header.Set("Authorization", "Basic "+auth)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: fetchTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("langfuse unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Cap the read: a 20-observation page with two 32K payloads is already
	// ~64KB, and anything beyond a few MB means something is wrong upstream.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("langfuse read: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		// A wrong key is a deployment problem, not a missing trace. Say so
		// loudly enough that it is not mistaken for "sampling dropped it".
		return nil, fmt.Errorf("langfuse rejected credentials: HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("langfuse HTTP %d", resp.StatusCode)
	}

	var payload struct {
		Data []rawObservation `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("langfuse response: %w", err)
	}
	if len(payload.Data) == 0 {
		return nil, ErrObservationNotFound
	}

	return mapObservation(pickRoot(payload.Data)), nil
}

// pickRoot selects the span that represents "this request".
func pickRoot(rows []rawObservation) rawObservation {
	for _, row := range rows {
		if row.IsRoot {
			return row
		}
	}
	// No root flag (older data, or a tree whose root is a client span we never
	// saw): the first row is the earliest span, which is the closest thing.
	return rows[0]
}

func mapObservation(row rawObservation) *Observation {
	obs := &Observation{
		ObservationID: row.ID,
		TraceID:       row.TraceID,
		Name:          row.Name,
		Type:          row.Type,
		Level:         row.Level,
		StatusMessage: row.StatusMessage,
		StartTime:     row.StartTime,
		EndTime:       row.EndTime,
		Model:         row.Model,
		Input:         anyToText(row.Input),
		Output:        anyToText(row.Output),
		PromptTokens:  row.InputUsage,
		OutputTokens:  row.OutputUsage,
		Environment:   row.Environment,
		Release:       row.Release,
	}
	if row.Latency != nil {
		obs.LatencySec = *row.Latency
	}
	if row.TimeToFirstToken != nil {
		obs.TtftSec = *row.TimeToFirstToken
	}

	// langfuse.trace.metadata.* lands at the first level of `metadata`
	// (filterable), while bare attributes are nested under `attributes.`.
	meta := row.Metadata
	obs.RequestID = metaString(meta, "request_id")
	obs.ChannelID = metaString(meta, "channel_id")
	obs.Group = metaString(meta, "group")
	obs.TokenName = metaString(meta, "token_name")
	obs.Format = metaString(meta, "format")

	// Content sampling is a tri-state: true, false, or absent (span written by
	// a build before the flag existed). Absence falls back to "did we get any
	// text", which is the only signal available then.
	if sampled, ok := metaBool(meta, "attributes.apiduo.content_sampled"); ok {
		obs.ContentSampled = sampled
	} else {
		obs.ContentSampled = obs.Input != "" || obs.Output != ""
	}

	return obs
}

// anyToText flattens a JSON field that Langfuse may store as a string or as a
// decoded object. Objects are re-encoded compactly: the drawer shows JSON.
func anyToText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// metaString reads one metadata key as text. Langfuse stores our numeric
// dimensions as strings already (they went out as stringValue), but older rows
// or a differently-typed export may hold numbers.
func metaString(meta map[string]any, key string) string {
	v, ok := meta[key]
	if !ok {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return ""
	}
}

// metaBool reads one metadata key as a flag, reporting whether it was present
// at all — "absent" and "false" drive different UI copy.
func metaBool(meta map[string]any, key string) (value bool, present bool) {
	v, ok := meta[key]
	if !ok {
		return false, false
	}
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		if x == "true" || x == "1" {
			return true, true
		}
		if x == "false" || x == "0" {
			return false, true
		}
	}
	return false, false
}

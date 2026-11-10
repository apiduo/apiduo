package trace

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// OTLP/HTTP JSON encoding for a batch of snapshots. Hand-rolled on purpose:
// one span per request with a fixed field set, so the generic OTEL SDK
// would add dependency weight without adding anything we use (docs §4).
//
// Langfuse reads the OTLP payload at /api/public/otel/v1/traces and maps
// attributes onto its trace model: langfuse.* keys are first-class, and
// gen_ai.* keys make the same payload usable by any OTel backend.

type jsonBuf struct {
	b strings.Builder
}

func (j *jsonBuf) str(s string) {
	j.b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			j.b.WriteString("\\\"")
		case '\\':
			j.b.WriteString("\\\\")
		case '\n':
			j.b.WriteString("\\n")
		case '\r':
			j.b.WriteString("\\r")
		case '\t':
			j.b.WriteString("\\t")
		default:
			if r < 0x20 {
				j.b.WriteString("\\u")
				const hexDigits = "0123456789abcdef"
				j.b.WriteByte('0')
				j.b.WriteByte('0')
				j.b.WriteByte(hexDigits[(r>>4)&0xf])
				j.b.WriteByte(hexDigits[r&0xf])
			} else {
				j.b.WriteRune(r)
			}
		}
	}
	j.b.WriteByte('"')
}

func (j *jsonBuf) attrStr(key, value string) {
	if value == "" {
		return
	}
	j.b.WriteString(`{"key":`)
	j.str(key)
	j.b.WriteString(`,"value":{"stringValue":`)
	j.str(value)
	j.b.WriteString("}},")
}

func (j *jsonBuf) attrInt(key string, value int64) {
	if value == 0 {
		return
	}
	j.b.WriteString(`{"key":`)
	j.str(key)
	j.b.WriteString(`,"value":{"intValue":"`)
	j.b.WriteString(strconv.FormatInt(value, 10))
	j.b.WriteString(`"}},`)
}

// attrBoolAlways emits both true and false. For flags where "false" is a
// meaningful state (content sampled or not), omitting it would make an old
// exporter and a content-stripped span look identical.
func (j *jsonBuf) attrBoolAlways(key string, value bool) {
	j.b.WriteString(`{"key":`)
	j.str(key)
	if value {
		j.b.WriteString(`,"value":{"boolValue":true}},`)
		return
	}
	j.b.WriteString(`,"value":{"boolValue":false}},`)
}

// attrStrArray emits an OTLP string array. Langfuse reads
// langfuse.trace.tags as string[] and — unlike langfuse.trace.metadata.*,
// which can only be filtered — exposes tags as a groupable dimension in the
// v2 metrics API. That is why carrying the same value in metadata does not
// make the tag redundant: one can be sliced, the other cannot.
func (j *jsonBuf) attrStrArray(key string, values []string) {
	if len(values) == 0 {
		return
	}
	j.b.WriteString(`{"key":`)
	j.str(key)
	j.b.WriteString(`,"value":{"arrayValue":{"values":[`)
	for i, v := range values {
		if i > 0 {
			j.b.WriteByte(',')
		}
		j.b.WriteString(`{"stringValue":`)
		j.str(v)
		j.b.WriteByte('}')
	}
	j.b.WriteString(`]}}},`)
}

func (j *jsonBuf) attrBool(key string, value bool) {
	if !value {
		return
	}
	j.b.WriteString(`{"key":`)
	j.str(key)
	j.b.WriteString(`,"value":{"boolValue":true}},`)
}

// binaryPlaceholder replaces non-text payloads (multipart image uploads,
// audio bytes) so Langfuse shows a readable marker instead of mojibake.
// Bytes >= 0x80 are allowed: UTF-8 text must survive; only C0 control
// characters (excluding \n \r \t) betray binary content.
func binaryPlaceholder(data []byte) string {
	for _, b := range data {
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' {
			return "[binary " + strconv.Itoa(len(data)) + " bytes]"
		}
	}
	return string(data)
}

// BuildExportBody encodes snapshots as one OTLP/HTTP JSON traces request.
// Parsing and redaction happen here, in the flush worker — the request path
// never runs either (docs §3.4 / §3.6).
func BuildExportBody(conf *Config, snaps []*Snapshot) []byte {
	var root strings.Builder
	root.WriteString(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"apiduo-gateway"}}]},"scopeSpans":[{"scope":{"name":"apiduo.trace","version":"1"},"spans":[`)
	for i, sn := range snaps {
		if i > 0 {
			root.WriteByte(',')
		}
		writeSpan(&root, conf, sn)
	}
	root.WriteString("]}]}]}")
	return []byte(root.String())
}

func writeSpan(b *strings.Builder, conf *Config, sn *Snapshot) {
	var j jsonBuf

	j.attrStr("langfuse.observation.type", "generation")
	j.attrStr("langfuse.trace.name", sn.Name)
	// Only langfuse.{trace,observation}.metadata.* land in Langfuse's top-level
	// metadata and become filterable — bare apiduo.* keys are demoted into
	// metadata.attributes and cannot be queried. These five are the dimensions
	// ops actually filters on ("which channel / key / group was slow"), so
	// they are duplicated here; the apiduo.* copies stay for raw payload
	// inspection and for anyone reading the OTLP directly.
	j.attrStr("langfuse.trace.metadata.request_id", sn.RequestID)
	j.attrStr("langfuse.trace.metadata.group", sn.Group)
	j.attrStr("langfuse.trace.metadata.token_name", sn.TokenName)
	j.attrStr("langfuse.trace.metadata.format", sn.Format)
	if sn.ChannelId > 0 {
		j.attrStr("langfuse.trace.metadata.channel_id", strconv.Itoa(sn.ChannelId))
	}
	// Trace-level context that stays constant for the deployment. Langfuse v4
	// keeps these on every observation row, so they work as filter columns
	// ("all spans from build X in environment Y") without touching metadata.
	j.attrStr("langfuse.release", conf.ReleaseName())
	j.attrStr("langfuse.environment", conf.Environment)
	// Grouping dimensions. Metadata entries above are filter-only, so without
	// these "p95 latency per channel" is not expressible in Langfuse at all.
	j.attrStrArray("langfuse.trace.tags", sn.tags())
	j.attrStr("gen_ai.operation.name", sn.Operation)
	// langfuse.observation.model.name wins over the gen_ai.* fallbacks.
	j.attrStr("langfuse.observation.model.name", sn.Model)
	j.attrStr("gen_ai.request.model", sn.Model)
	j.attrStr("gen_ai.response.model", sn.Model)
	j.attrInt("gen_ai.usage.input_tokens", int64(sn.PromptTokens))
	j.attrInt("gen_ai.usage.output_tokens", int64(sn.CompletionTokens))
	// completion_start_time is the field Langfuse renders as "time to first
	// token". apiduo already measures TTFT; sending it as a custom attribute
	// only left it in metadata.attributes, where the UI never reads it.
	if sn.TtftMs > 0 && sn.StartUnixNano > 0 {
		j.attrStr("langfuse.observation.completion_start_time",
			time.Unix(0, sn.StartUnixNano+sn.TtftMs*int64(time.Millisecond)).
				UTC().Format("2006-01-02T15:04:05.000Z"))
	}
	j.attrStr("langfuse.user.id", strconv.Itoa(sn.UserId))
	j.attrStr("apiduo.request_id", sn.RequestID)
	j.attrStr("apiduo.group", sn.Group)
	j.attrStr("apiduo.token_name", sn.TokenName)
	j.attrStr("apiduo.format", sn.Format)
	j.attrInt("apiduo.channel_id", int64(sn.ChannelId))
	j.attrInt("apiduo.quota", int64(sn.Quota))
	j.attrInt("apiduo.retry", int64(sn.Retry))
	j.attrBool("apiduo.is_stream", sn.IsStream)
	j.attrBoolAlways("apiduo.content_sampled", sn.ContentSampled)
	j.attrBool("apiduo.interrupted", sn.Interrupted)
	j.attrBool("apiduo.input_truncated", sn.InputTruncated)
	j.attrBool("apiduo.output_truncated", sn.OutputTruncated)
	j.attrInt("apiduo.input_dropped_bytes", int64(sn.InputDropped))
	j.attrInt("apiduo.output_dropped_bytes", int64(sn.OutputDropped))
	if sn.TtftMs > 0 {
		j.attrInt("apiduo.ttft_ms", sn.TtftMs)
	}
	if len(sn.Input) > 0 {
		text := redactForExport(conf, binaryPlaceholder(sn.Input))
		j.attrStr("langfuse.observation.input", text)
		j.attrStr("gen_ai.prompt", text)
	}
	if len(sn.Output) > 0 {
		text := redactForExport(conf, formatOutput(sn))
		j.attrStr("langfuse.observation.output", text)
		j.attrStr("gen_ai.completion", text)
	}
	// Langfuse surfaces level in the trace list and status_message in the
	// detail pane; without them an error span looks like a healthy one.
	if !sn.Success {
		j.attrStr("langfuse.observation.level", "ERROR")
		if sn.ErrorText != "" {
			j.attrStr("langfuse.observation.status_message", sn.ErrorText)
		}
	}
	attrs := j.b.String()
	attrs = strings.TrimSuffix(attrs, ",")

	b.WriteString(`{"traceId":`)
	b.WriteString(strconv.Quote(sn.TraceID))
	b.WriteString(`,"spanId":`)
	b.WriteString(strconv.Quote(sn.SpanID))
	if sn.ParentSpanID != "" {
		b.WriteString(`,"parentSpanId":`)
		b.WriteString(strconv.Quote(sn.ParentSpanID))
	}
	b.WriteString(`,"name":`)
	b.WriteString(strconv.Quote(sn.Name))
	b.WriteString(`,"kind":1,"startTimeUnixNano":"`)
	b.WriteString(strconv.FormatInt(sn.StartUnixNano, 10))
	b.WriteString(`","endTimeUnixNano":"`)
	b.WriteString(strconv.FormatInt(sn.EndUnixNano, 10))
	b.WriteString(`","attributes":[`)
	b.WriteString(attrs)
	b.WriteString(`],"status":{"code":`)
	if sn.Success {
		b.WriteString("1")
	} else {
		b.WriteString("2")
		if sn.ErrorText != "" {
			b.WriteString(`,"message":`)
			b.WriteString(strconv.Quote(sn.ErrorText))
		}
	}
	b.WriteString("}}")
}

// formatOutput prefers a parsed view (text + tool calls + finish reason) and
// falls back to raw frames when no parser claims the format. Parsing keeps
// the payload readable in Langfuse and shrinks it: a parsed answer is the
// text itself instead of hundreds of JSON frames.
func formatOutput(sn *Snapshot) string {
	raw := binaryPlaceholder(sn.Output)
	parsed, ok := parseOutput(sn.Format, raw)
	if !ok {
		return raw
	}
	m := map[string]any{"text": parsed.Text}
	if len(parsed.ToolCalls) > 0 {
		m["tool_calls"] = parsed.ToolCalls
	}
	if parsed.FinishReason != "" {
		m["finish_reason"] = parsed.FinishReason
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return string(encoded)
}

// tags lists the dimensions ops actually charts by. Channel and group are
// per-request attributes; the stream flag is included because streaming and
// buffered requests have systematically different latency distributions, so
// one blended p95 describes neither. Values stay low-cardinality on purpose:
// a request id in here would explode the dimension into uselessness.
func (sn *Snapshot) tags() []string {
	tags := make([]string, 0, 3)
	if sn.ChannelId > 0 {
		tags = append(tags, "channel:"+strconv.Itoa(sn.ChannelId))
	}
	if sn.Group != "" {
		tags = append(tags, "group:"+sn.Group)
	}
	if sn.IsStream {
		tags = append(tags, "stream")
	} else {
		tags = append(tags, "non-stream")
	}
	return tags
}

// maxErrorTextRunes bounds the failure reason we ship. Upstream error bodies
// can be huge; the first few hundred characters are what a human reads.
const maxErrorTextRunes = 400

// clipRunes truncates on a rune boundary so a multi-byte character is never
// split into invalid UTF-8 (the payload is JSON, and half a rune would decode
// as U+FFFD in Langfuse).
func clipRunes(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

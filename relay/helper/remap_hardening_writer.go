package helper

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// maxBufferedRemapBody bounds how much of a non-streaming response the writer
// holds in memory. A chat completion larger than this is not something the
// gateway is willing to buffer; refusing it is better than forwarding bytes it
// could not rewrite, because forwarding is the leak this writer exists to stop.
const maxBufferedRemapBody = 8 << 20

// RemapHardeningWriter reports the client's own model name in the response and
// removes upstream fingerprints, but only for requests that the channel's
// model_mapping actually rewrote.
//
// The writer is installed before the relay handler runs, while IsModelMapped is
// still unknown: ModelMappedHelper executes deeper, per handler. The decision is
// therefore deferred to the first write. When either the channel flag or the
// mapping is absent the writer stays transparent and every method forwards.
type RemapHardeningWriter struct {
	gin.ResponseWriter
	resolve func() (public string, enabled bool)

	decided bool
	active  bool
	sse     bool
	public  string

	status     int
	headerSent bool
	carry      []byte
	body       bytes.Buffer
	overflow   bool

	// gatewayHeaderKeys are the headers the distributor and middlewares set
	// before the upstream was contacted. Anything else on the writer once the
	// reply arrives came from the vendor.
	gatewayHeaderKeys map[string]bool
}

// NewRemapHardeningWriter wraps w. resolve is called once, on the first write,
// because whether the channel mapping matched is only known after the handler's
// mapping helper ran - which is after this constructor.
func NewRemapHardeningWriter(w gin.ResponseWriter, resolve func() (string, bool)) *RemapHardeningWriter {
	gatewayHeaderKeys := make(map[string]bool, len(w.Header()))
	for key := range w.Header() {
		gatewayHeaderKeys[strings.ToLower(key)] = true
	}
	return &RemapHardeningWriter{
		ResponseWriter:    w,
		resolve:           resolve,
		status:            http.StatusOK,
		gatewayHeaderKeys: gatewayHeaderKeys,
	}
}

// clientHeaderAllowlist is what a client protocol actually needs. Everything
// else that appears after the upstream replied is vendor fingerprinting:
// Server, Date, Set-Cookie, Openai-Organization, X-Amzn-RequestId, Cf-Ray and
// whatever a given provider invents next.
var clientHeaderAllowlist = map[string]bool{
	"content-type":        true,
	"content-encoding":    true,
	"content-disposition": true,
	"cache-control":       true,
	"pragma":              true,
	"expires":             true,
	"retry-after":         true,
	// Set inside Relay, after the snapshot: without it nginx buffers the SSE
	// stream and the first token arrives only when the whole answer is done.
	"x-accel-buffering":   true,
}

// dropUpstreamHeaders removes headers that neither the client protocol needs
// nor the gateway set itself before contacting the vendor.
//
// IOCopyBytesGracefully forwards every upstream header except Content-Length
// and the upstream request id, and it runs before this writer sees the status,
// so the vendor's identity is already on the writer by the time sendHeader
// runs.
func (w *RemapHardeningWriter) dropUpstreamHeaders() {
	header := w.ResponseWriter.Header()
	for key := range header {
		lower := strings.ToLower(key)
		// The build string embeds the deployment timestamp, which dates the
		// instance for anyone fingerprinting it. It is not needed to use the
		// API, so a hardened channel stops advertising it.
		if lower == "x-new-api-version" {
			header.Del(key)
			continue
		}
		if clientHeaderAllowlist[lower] || w.gatewayHeaderKeys[lower] {
			continue
		}
		header.Del(key)
	}
}

// Unwrap exposes the wrapped writer so the helper lookups that walk the writer
// chain (keep-alive reuse, flush error propagation) keep working, the same way
// streamKeepAliveWriter does.
func (w *RemapHardeningWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// decide resolves whether this response is subject to rewriting. It is cheap
// and idempotent, so every entry point can call it.
func (w *RemapHardeningWriter) decide() {
	if w.decided {
		return
	}
	w.decided = true
	if w.resolve == nil {
		return
	}
	public, enabled := w.resolve()
	if !enabled || public == "" {
		return
	}
	w.public = public
	w.active = true
}

func (w *RemapHardeningWriter) contentTypeIsSSE() bool {
	return strings.Contains(w.ResponseWriter.Header().Get("Content-Type"), "text/event-stream")
}

// sendHeader commits the status and headers. Content-Length is dropped because
// rewriting the model name changes the body length, and a stale length would
// truncate the response.
func (w *RemapHardeningWriter) sendHeader() {
	if w.headerSent {
		return
	}
	w.dropUpstreamHeaders()
	w.ResponseWriter.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeader(w.status)
	w.headerSent = true
}

func (w *RemapHardeningWriter) WriteHeader(code int) {
	w.status = code
	w.decide()
	if !w.active {
		w.ResponseWriter.WriteHeader(code)
		w.headerSent = true
		return
	}
	// A streaming handler sets the content type before writing the status, so
	// the header can be committed now. A non-streaming body is held back until
	// Finalize, which is also the last chance to replace an oversized response
	// with an error.
	if w.contentTypeIsSSE() {
		w.sse = true
		w.sendHeader()
	}
}

func (w *RemapHardeningWriter) WriteHeaderNow() {
	w.decide()
	if !w.active {
		w.ResponseWriter.WriteHeaderNow()
		return
	}
	if w.sse || w.contentTypeIsSSE() {
		w.sse = true
		w.sendHeader()
	}
}

func (w *RemapHardeningWriter) Write(b []byte) (int, error) {
	w.decide()
	if !w.active {
		return w.ResponseWriter.Write(b)
	}
	// SSE comments carry no identity, and a keep-alive ping exists precisely to
	// reach the client before the first token. Once the response is known to be
	// a stream the normal path already passes comments through unchanged.
	if !w.sse && isSSEComment(b) {
		w.sse = true
		w.sendHeader()
		return w.ResponseWriter.Write(b)
	}
	if w.sse || w.contentTypeIsSSE() {
		w.sse = true
		w.sendHeader()
		return w.writeStream(b)
	}
	if w.body.Len()+len(b) > maxBufferedRemapBody {
		w.overflow = true
		return len(b), nil
	}
	return w.body.Write(b)
}

func isSSEComment(b []byte) bool {
	trimmed := bytes.TrimLeft(b, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == ':'
}

func (w *RemapHardeningWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

func (w *RemapHardeningWriter) Flush() {
	w.decide()
	if !w.active {
		w.ResponseWriter.Flush()
		return
	}
	// A keep-alive ping can flush before the first body write; resolve the
	// stream shape here so the ping is not swallowed.
	if !w.sse && w.contentTypeIsSSE() {
		w.sse = true
		w.sendHeader()
	}
	if !w.sse {
		// A buffered body must not reach the client before Finalize: a partial
		// body is exactly the unrewritten bytes this writer hides.
		return
	}
	_ = w.flushStreamLines()
	w.ResponseWriter.Flush()
}

func (w *RemapHardeningWriter) Written() bool {
	w.decide()
	if !w.active {
		return w.ResponseWriter.Written()
	}
	return w.headerSent
}

func (w *RemapHardeningWriter) Status() int {
	w.decide()
	if !w.active {
		return w.ResponseWriter.Status()
	}
	return w.status
}

func (w *RemapHardeningWriter) Size() int {
	w.decide()
	if !w.active {
		return w.ResponseWriter.Size()
	}
	if w.sse {
		return w.ResponseWriter.Size()
	}
	return w.body.Len()
}

// Finalize writes the buffered body. It must run after every other deferred
// hook, so that an error rendered on the way out is rewritten too.
func (w *RemapHardeningWriter) Finalize() {
	w.decide()
	if !w.active || w.sse {
		return
	}
	if w.overflow {
		w.status = http.StatusBadGateway
		w.sendHeader()
		_, _ = w.ResponseWriter.Write([]byte(
			`{"error":{"message":"The upstream response was too large to rewrite.","type":"server_error","code":"upstream_response_too_large"}}`,
		))
		return
	}
	raw := w.body.Bytes()
	w.body.Reset()
	body := raw
	if len(raw) > 0 {
		if rewritten, changed := rewriteUpstreamIdentity(raw, w.public); changed {
			body = rewritten
		}
	}
	w.sendHeader()
	if len(body) > 0 {
		_, _ = w.ResponseWriter.Write(body)
	}
}

// writeStream rewrites the events that are complete and keeps the tail, so a
// line split across writes is never inspected half-way.
func (w *RemapHardeningWriter) writeStream(b []byte) (int, error) {
	w.carry = append(w.carry, b...)
	if err := w.flushStreamLines(); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (w *RemapHardeningWriter) flushStreamLines() error {
	last := bytes.LastIndexByte(w.carry, '\n')
	if last < 0 {
		return nil
	}
	head := w.carry[:last+1]
	tail := append([]byte(nil), w.carry[last+1:]...)
	w.carry = tail
	out := rewriteSSELines(head, w.public)
	if len(out) == 0 {
		return nil
	}
	_, err := w.ResponseWriter.Write(out)
	return err
}

func rewriteSSELines(src []byte, public string) []byte {
	var out bytes.Buffer
	out.Grow(len(src))
	for len(src) > 0 {
		lineEnd := bytes.IndexByte(src, '\n')
		var line []byte
		if lineEnd < 0 {
			line, src = src, nil
		} else {
			line, src = src[:lineEnd+1], src[lineEnd+1:]
		}
		out.Write(rewriteSSELine(line, public))
	}
	return out.Bytes()
}

func rewriteSSELine(line []byte, public string) []byte {
	trimmed := bytes.TrimRight(line, "\r\n")
	trailer := line[len(trimmed):]
	payload, ok := bytes.CutPrefix(trimmed, []byte("data:"))
	if !ok {
		return line
	}
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return line
	}
	rewritten, changed := rewriteUpstreamIdentity(payload, public)
	if !changed {
		return line
	}
	out := make([]byte, 0, len(rewritten)+8+len(trailer))
	out = append(out, "data: "...)
	out = append(out, rewritten...)
	out = append(out, trailer...)
	return out
}

// rewriteUpstreamIdentity replaces the identity fields a client must not see.
//
// It works on bytes rather than by decoding: the fields are fixed keys holding
// a string, and the escaping that makes a JSON string safe also makes this
// scan safe. A quoted "model" inside content is written \"model\", which never
// matches the unescaped key; a different key such as "model_version" does not
// match either, because the key must be followed by a closing quote.
func rewriteUpstreamIdentity(payload []byte, public string) ([]byte, bool) {
	// Only an object can carry these fields. Anything else (an HTML error page
	// relayed verbatim, a bare string) is left untouched rather than risking a
	// rewrite of prose that happens to contain the pattern.
	if trimmed := bytes.TrimLeft(payload, " \t\r\n"); len(trimmed) == 0 || trimmed[0] != '{' {
		return payload, false
	}
	out := payload
	changed := false
	for range 4 {
		next, replaced := replaceJSONStringValue(out, "model", public)
		if !replaced {
			break
		}
		out, changed = next, true
	}
	if next, replaced := replaceJSONStringValue(out, "modelVersion", public); replaced {
		out, changed = next, true
	}
	// Provider fingerprint plus the three provenance fields the conversion
	// layer writes into usage. All four are gateway accounting state: no vendor
	// returns them, and each one names the dialect or wiring behind the answer.
	for _, key := range []string{
		"system_fingerprint",
		"billing_usage",
		"usage_semantic",
		"usage_source",
	} {
		if next, replaced := nullJSONField(out, key); replaced {
			out, changed = next, true
		}
	}
	if !changed {
		return payload, false
	}
	return out, true
}

// findJSONValueStart locates the first byte of the value for key.
func findJSONValueStart(src []byte, key string) (int, bool) {
	needle := []byte(`"` + key + `"`)
	for i := 0; i+len(needle) <= len(src); i++ {
		if !bytes.Equal(src[i:i+len(needle)], needle) {
			continue
		}
		j := i + len(needle)
		for j < len(src) && (src[j] == ' ' || src[j] == '\t') {
			j++
		}
		if j >= len(src) || src[j] != ':' {
			continue
		}
		j++
		for j < len(src) && (src[j] == ' ' || src[j] == '\t') {
			j++
		}
		if j < len(src) {
			return j, true
		}
	}
	return 0, false
}

// findJSONValueEnd returns the exclusive end of the JSON value starting at
// start. Strings honour escapes; objects and arrays are balanced, skipping
// braces that appear inside string literals.
func findJSONValueEnd(src []byte, start int) int {
	if start >= len(src) {
		return -1
	}
	switch src[start] {
	case '"':
		for i := start + 1; i < len(src); i++ {
			if src[i] == '\\' {
				i++
				continue
			}
			if src[i] == '"' {
				return i + 1
			}
		}
		return -1
	case '{', '[':
		depth := 0
		for i := start; i < len(src); i++ {
			switch src[i] {
			case '"':
				for i++; i < len(src); i++ {
					if src[i] == '\\' {
						i++
						continue
					}
					if src[i] == '"' {
						break
					}
				}
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
		}
		return -1
	default:
		for i := start; i < len(src); i++ {
			switch src[i] {
			case ',', '}', ']', ' ', '\t', '\r', '\n':
				return i
			}
		}
		return len(src)
	}
}

func findJSONStringValue(src []byte, key string) (int, int, bool) {
	start, ok := findJSONValueStart(src, key)
	if !ok || src[start] != '"' {
		return 0, 0, false
	}
	end := findJSONValueEnd(src, start)
	if end < 0 {
		return 0, 0, false
	}
	return start, end, true
}

func replaceJSONStringValue(src []byte, key, value string) ([]byte, bool) {
	start, end, ok := findJSONStringValue(src, key)
	if !ok {
		return src, false
	}
	if bytes.Equal(src[start+1:end-1], []byte(value)) {
		return src, false
	}
	quoted := strconv.AppendQuote(nil, value)
	out := make([]byte, 0, len(src)-(end-start)+len(quoted))
	out = append(out, src[:start]...)
	out = append(out, quoted...)
	out = append(out, src[end:]...)
	return out, true
}

// nullJSONField replaces any JSON value with null. Nulling keeps the key for
// clients that read it while carrying nothing: the fingerprint names the
// provider configuration, and billing_usage names the wiring behind the
// conversion (source: oai_chat, semantic: openai).
func nullJSONField(src []byte, key string) ([]byte, bool) {
	start, ok := findJSONValueStart(src, key)
	if !ok {
		return src, false
	}
	end := findJSONValueEnd(src, start)
	if end < 0 {
		return src, false
	}
	if end-start == 4 && bytes.Equal(src[start:end], []byte("null")) {
		return src, false
	}
	out := make([]byte, 0, len(src)-(end-start)+4)
	out = append(out, src[:start]...)
	out = append(out, "null"...)
	out = append(out, src[end:]...)
	return out, true
}

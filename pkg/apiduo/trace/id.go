package trace

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// TraceID derives the 32-hex-character OTEL trace id from new-api's request
// id. The request id itself is 39 characters (timestamp + nanos + build
// prefix + random) and cannot be used directly; a stable one-way derivation
// keeps log page and Langfuse in sync without storing anything extra.
//
// The derivation must stay byte-for-byte stable: the log page link and the
// exporter both rely on it, and tests pin it (TestTraceIDStable).
func TraceID(requestID string) string {
	if requestID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("apiduo-trace\x00" + requestID))
	return hex.EncodeToString(sum[:16])
}

// SpanID derives the 16-hex-character span id from the same request id,
// using different bytes of the same digest (cheap, collision-free enough for
// one request).
func SpanID(requestID string) string {
	if requestID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("apiduo-trace\x00" + requestID))
	return hex.EncodeToString(sum[16:24])
}

// ParsedTraceparent carries the W3C Trace Context fields we care about.
type ParsedTraceparent struct {
	TraceID      string
	ParentSpanID string
	Sampled      bool
}

// ParseTraceparent parses "00-<32hex>-<16hex>-<2hex>". It returns ok=false
// for anything malformed; callers fall back to the derived trace id.
func ParseTraceparent(header string) (ParsedTraceparent, bool) {
	var p ParsedTraceparent
	if header == "" {
		return p, false
	}
	rest, ok := strings.CutPrefix(header, "00-")
	if !ok {
		return p, false
	}
	tracePart, rest, ok := strings.Cut(rest, "-")
	if !ok || len(tracePart) != 32 || !isHex(tracePart) {
		return p, false
	}
	spanPart, flagsPart, ok := strings.Cut(rest, "-")
	if !ok || len(spanPart) != 16 || !isHex(spanPart) {
		return p, false
	}
	if len(flagsPart) != 2 || !isHex(flagsPart) {
		return p, false
	}
	p.TraceID = tracePart
	p.ParentSpanID = spanPart
	// W3C: the low bit of the flags octet is the sampled bit.
	p.Sampled = flagsPart[1] == '1' || flagsPart[1] == '3' || flagsPart[1] == '5' || flagsPart[1] == '7' ||
		flagsPart[1] == '9' || flagsPart[1] == 'b' || flagsPart[1] == 'd' || flagsPart[1] == 'f'
	return p, true
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

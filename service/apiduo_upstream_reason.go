package service

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// apiduoUpstreamReasonLimit bounds the vendor text a task failure reason may
// carry. Long enough for a vendor's own sentence, short enough that a client
// rendering the reason is not handed a document.
const apiduoUpstreamReasonLimit = 300

// SanitizeUpstreamReason renders a vendor error body as one readable line.
//
// Vendors answer failures with whatever their edge produced: an ALB's HTML 502
// page, a Cloudflare interstitial, a multi-kilobyte JSON dump. That text used
// to become the task's failure reason verbatim, so a client polling a failed
// task received a whole web page inside `error.message`. Keeping the vendor's
// own JSON message and replacing markup with a one-line verdict keeps the
// reason actionable.
func SanitizeUpstreamReason(text string, statusCode int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	// A JSON envelope is left alone: the presenters above parse its code and
	// message, and rewriting it here would turn a structured vendor error into
	// a bare sentence they can no longer read.
	if apiduoIsJSONObject(text) {
		return text
	}
	if apiduoLooksLikeMarkup(text) {
		if statusCode > 0 {
			return fmt.Sprintf("upstream returned an HTML error page (HTTP %d)", statusCode)
		}
		return "upstream returned an HTML error page"
	}
	if compact := strings.Join(strings.Fields(text), " "); compact != "" {
		text = compact
	}
	runes := []rune(text)
	if len(runes) <= apiduoUpstreamReasonLimit {
		return text
	}
	return strings.TrimSpace(string(runes[:apiduoUpstreamReasonLimit])) + "…"
}

// apiduoIsJSONObject reports whether the text is a parseable JSON object, the
// shape every presenter and client SDK knows how to read.
func apiduoIsJSONObject(text string) bool {
	if !strings.HasPrefix(text, "{") {
		return false
	}
	var payload map[string]any
	return common.Unmarshal([]byte(text), &payload) == nil
}

func apiduoLooksLikeMarkup(text string) bool {
	lower := strings.ToLower(text)
	return strings.HasPrefix(lower, "<") ||
		strings.Contains(lower, "<html") ||
		strings.Contains(lower, "<!doctype")
}

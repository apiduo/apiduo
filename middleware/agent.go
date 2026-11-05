package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// agentHeader carries a self-declared label for an automated client, for
// example `X-Agent-Name: workbuddy-deploy`. It lets the audit trail answer
// "which of my agents did this" once several share one account token.
const agentHeader = "X-Agent-Name"

// agentLabelMaxRunes bounds what reaches the audit row; a runaway header would
// otherwise bloat every entry it touches.
const agentLabelMaxRunes = 64

// Attribution sources, stored next to the label so a reader can tell a
// declaration from a guess.
const (
	AgentSourceHeader    = "header"
	AgentSourceUserAgent = "user_agent"
)

// AgentAttribution is who the request claims to be, and how confident that is.
type AgentAttribution struct {
	Label  string
	Source string
}

// AttributeAgent resolves the agent behind a request.
//
// Order matters: an explicit header always wins, because it is a statement
// about the specific client rather than a family. Only when it is absent do we
// fall back to recognising well-known User-Agent markers, which is a guess and
// is labelled as such. Anything unrecognised stays empty — filling in a default
// would attribute browser sessions and ad-hoc scripts to whichever tool happens
// to be most common, which is worse than saying nothing.
func AttributeAgent(c *gin.Context) AgentAttribution {
	if label := normalizeAgentLabel(c.GetHeader(agentHeader)); label != "" {
		return AgentAttribution{Label: label, Source: AgentSourceHeader}
	}
	if label := agentFromUserAgent(c.Request.UserAgent()); label != "" {
		return AgentAttribution{Label: label, Source: AgentSourceUserAgent}
	}
	return AgentAttribution{}
}

// agentUserAgentMarkers maps a substring of the User-Agent to the label it
// implies.
//
// Order matters: WorkBuddy 5.9 sends
//
//	WorkBuddy/5.9.3 WorkBuddy/5.9.3 OpenAI/Python 1.99.0 CLI/2.165.0
//
// (platform/version, product/version, then the wrapped SDK, then an extension
// appended by its HTTP interceptor). Matching the outer product first reports
// the tool the operator runs, not the library underneath it.
var agentUserAgentMarkers = []struct {
	marker string
	label  string
}{
	{"workbuddy", "workbuddy"},
	{"codebuddy", "workbuddy"},
	{"codex", "codex"},
	{"claude-code", "claude-code"},
	{"claude-cli", "claude-code"},
	{"claude_cli", "claude-code"},
	{"cursor", "cursor"},
	{"cline", "cline"},
	{"aider", "aider"},
}

func agentFromUserAgent(ua string) string {
	if ua == "" {
		return ""
	}
	lower := strings.ToLower(ua)
	for _, entry := range agentUserAgentMarkers {
		if strings.Contains(lower, entry.marker) {
			return entry.label
		}
	}
	return ""
}

// normalizeAgentLabel trims and bounds a caller-supplied label.
func normalizeAgentLabel(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	// Fast path: a short printable-ASCII label needs neither truncation nor
	// cleaning, which is the overwhelmingly common case. Returning the trimmed
	// string directly keeps this allocation-free — it runs on every request that
	// carries a token.
	if len(raw) <= agentLabelMaxRunes && isPrintableASCII(raw) {
		return raw
	}

	out := make([]rune, 0, agentLabelMaxRunes)
	for _, r := range raw {
		if len(out) >= agentLabelMaxRunes {
			break
		}
		// Drop control characters so a label cannot forge line breaks in log
		// output or break JSON consumers downstream.
		if r < 0x20 || r == 0x7f {
			continue
		}
		out = append(out, r)
	}

	return strings.TrimSpace(string(out))
}

// AgentLabel returns only the label, for callers that do not store the source.
func AgentLabel(c *gin.Context) string {
	return AttributeAgent(c).Label
}

// isPrintableASCII reports whether every byte is ordinary printable ASCII.
// Anything else (control characters, multi-byte UTF-8) takes the slow path,
// which counts runes properly.
func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if b := s[i]; b < 0x20 || b >= 0x7f {
			return false
		}
	}
	return true
}

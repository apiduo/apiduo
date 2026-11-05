package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func agentRequest(t *testing.T, header string) *gin.Context {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	if header != "" {
		c.Request.Header.Set(agentHeader, header)
	}
	return c
}

func TestAgentLabelReadsTheHeader(t *testing.T) {
	assert.Equal(t, "codex-refactor", AgentLabel(agentRequest(t, "codex-refactor")))
	assert.Equal(t, "", AgentLabel(agentRequest(t, "")))
	assert.Equal(t, "", AgentLabel(agentRequest(t, "   ")))
}

func TestAgentLabelIsBounded(t *testing.T) {
	label := AgentLabel(agentRequest(t, strings.Repeat("a", 200)))
	assert.Len(t, []rune(label), agentLabelMaxRunes)
}

func TestAgentLabelStripsControlCharacters(t *testing.T) {
	// A label with an embedded newline must not be able to forge a second line
	// in a log viewer or break a JSON consumer downstream.
	label := AgentLabel(agentRequest(t, "codex\nfake-entry\r\t"))
	assert.Equal(t, "codexfake-entry", label)
}

func TestAgentLabelHandlesMultiByteLabels(t *testing.T) {
	// Multi-byte labels take the slow path, which counts runes rather than
	// bytes; a byte-based cap would cut a Chinese name in half.
	assert.Equal(t, "编排助手", AgentLabel(agentRequest(t, "编排助手")))
	assert.Equal(t, "codex🤖", AgentLabel(agentRequest(t, "codex🤖")))

	long := strings.Repeat("智能体", 30) // 90 runes, 270 bytes
	assert.Len(t, []rune(AgentLabel(agentRequest(t, long))), agentLabelMaxRunes)
}

func agentRequestWithUA(t *testing.T, header, ua string) *gin.Context {
	t.Helper()
	c := agentRequest(t, header)
	if ua != "" {
		c.Request.Header.Set("User-Agent", ua)
	}
	return c
}

func TestAttributeAgentPrefersTheDeclaredHeader(t *testing.T) {
	// A declaration about this specific client must beat a family-level guess.
	got := AttributeAgent(agentRequestWithUA(t, "workbuddy-deploy", "codex_cli_rs/0.9.0"))
	assert.Equal(t, AgentAttribution{Label: "workbuddy-deploy", Source: AgentSourceHeader}, got)
}

func TestAttributeAgentRecognisesKnownClients(t *testing.T) {
	cases := []struct{ ua, label string }{
		{"WorkBuddy/2.1.0 (darwin; arm64)", "workbuddy"},
		{"CodeBuddy/1.0", "workbuddy"},
		{"codex_cli_rs/0.20.0 (Mac OS 15.0)", "codex"},
		{"claude-cli/1.0.17 (external, cli)", "claude-code"},
		{"Cursor/0.42.3", "cursor"},
		{"aider/0.60.1", "aider"},
	}
	for _, test := range cases {
		got := AttributeAgent(agentRequestWithUA(t, "", test.ua))
		assert.Equal(t, test.label, got.Label, test.ua)
		assert.Equal(t, AgentSourceUserAgent, got.Source, test.ua)
	}
}

func TestAttributeAgentPrefersTheOuterProductOverTheTransport(t *testing.T) {
	// WorkBuddy can ship a Codex-compatible transport; the audit should name the
	// product the operator runs, not the library underneath it.
	got := AttributeAgent(agentRequestWithUA(t, "", "WorkBuddy/2.0 codex_cli_rs/0.9.0"))
	assert.Equal(t, "workbuddy", got.Label)
}

func TestAttributeAgentLeavesUnknownClientsEmpty(t *testing.T) {
	// Guessing wrong is worse than saying nothing: a browser session or a
	// throwaway script must not be attributed to the most common tool.
	for _, ua := range []string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/131.0",
		"python-urllib/3.13",
		"Go-http-client/2.0",
		"curl/8.7.1",
		"",
	} {
		got := AttributeAgent(agentRequestWithUA(t, "", ua))
		assert.Empty(t, got.Label, ua)
		assert.Empty(t, got.Source, ua)
	}
}

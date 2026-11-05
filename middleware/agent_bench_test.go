package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// AgentLabel runs on every request that carries a token, so its cost has to
// stay in the noise compared with the token lookup it accompanies.

func benchContext(header string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	if header != "" {
		c.Request.Header.Set(agentHeader, header)
	}
	return c
}

func BenchmarkAgentLabelNoHeader(b *testing.B) {
	c := benchContext("")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = AgentLabel(c)
	}
}

func BenchmarkAgentLabelShort(b *testing.B) {
	c := benchContext("codex-refactor")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = AgentLabel(c)
	}
}

func BenchmarkAgentLabelLong(b *testing.B) {
	c := benchContext(strings.Repeat("agent-name-", 20))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = AgentLabel(c)
	}
}

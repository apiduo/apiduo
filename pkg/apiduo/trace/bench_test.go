package trace

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// The disabled path is the default state in production, so it must stay at
// "one atomic load, zero allocations". Budget (docs §9.1): < 2 ns/op,
// 0 allocs/op. If this regresses, something in the fast path started
// allocating before checking Enabled().
func BenchmarkBeginDisabled(b *testing.B) {
	setConfig(&Config{Enabled: false})
	gin.SetMode(gin.TestMode)
	c := &gin.Context{}
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		Begin(c)
	}
}

func BenchmarkFillUsageDisabled(b *testing.B) {
	setConfig(&Config{Enabled: false})
	gin.SetMode(gin.TestMode)
	c := &gin.Context{}
	c.Request = httptest.NewRequest("POST", "/", nil)
	u := Usage{PromptTokens: 1, CompletionTokens: 2, Quota: 3}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		FillUsage(c, u)
	}
}

func BenchmarkFinalizeRelayDisabled(b *testing.B) {
	setConfig(&Config{Enabled: false})
	gin.SetMode(gin.TestMode)
	c := &gin.Context{}
	c.Request = httptest.NewRequest("POST", "/", nil)
	m := RelayMeta{UserId: 1, Model: "m", Group: "g", Success: true, StartUnixNano: 1}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		FinalizeRelay(c, m)
	}
}

func BenchmarkWrapStreamHandlerDisabled(b *testing.B) {
	setConfig(&Config{Enabled: false})
	gin.SetMode(gin.TestMode)
	c := &gin.Context{}
	c.Request = httptest.NewRequest("POST", "/", nil)
	h := func(data string, sr any) {}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		wrapped := WrapStreamHandler(c, h)
		wrapped("frame", nil)
	}
}

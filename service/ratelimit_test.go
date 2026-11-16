package service

import (
	"net/http"
	"testing"
)

// 「满」判据 1：两套上游的响应头都要认。
func TestParseRateLimitHeaders(t *testing.T) {
	openai := http.Header{}
	openai.Set("X-Ratelimit-Limit-Requests", "1000")
	openai.Set("X-Ratelimit-Remaining-Requests", "120")
	openai.Set("X-Ratelimit-Reset-Requests", "30s")

	got, ok := parseRateLimitHeaders(openai)
	if !ok {
		t.Fatal("openai headers not parsed")
	}
	if got.Limit != 1000 || got.Remaining != 120 || got.ResetRaw != "30s" || got.Source != "openai" {
		t.Errorf("openai: %+v", got)
	}

	anthropic := http.Header{}
	anthropic.Set("anthropic-ratelimit-requests-limit", "50")
	anthropic.Set("anthropic-ratelimit-requests-remaining", "5")

	got2, ok2 := parseRateLimitHeaders(anthropic)
	if !ok2 {
		t.Fatal("anthropic headers not parsed")
	}
	if got2.Limit != 50 || got2.Remaining != 5 || got2.Source != "anthropic" {
		t.Errorf("anthropic: %+v", got2)
	}

	// 没有相关头时不能假装有数据
	if _, ok3 := parseRateLimitHeaders(http.Header{}); ok3 {
		t.Error("empty header should not produce a snapshot")
	}

	// 只会漏报不会误报：值不是数字就跳过
	bad := http.Header{}
	bad.Set("x-ratelimit-remaining-requests", "n/a")
	if _, ok4 := parseRateLimitHeaders(bad); ok4 {
		t.Error("non-numeric remaining should be skipped")
	}
}

// 「满」判据 2：快照按剩余比例升序，最紧张的排最前。
func TestSnapshotRateLimitsOrder(t *testing.T) {
	rateLimitMu.Lock()
	rateLimitCache = map[int]RateLimitSnapshot{}
	rateLimitMu.Unlock()

	h1 := http.Header{}
	h1.Set("x-ratelimit-limit-requests", "100")
	h1.Set("x-ratelimit-remaining-requests", "80")
	ObserveRateLimit(1, h1)

	h2 := http.Header{}
	h2.Set("x-ratelimit-limit-requests", "100")
	h2.Set("x-ratelimit-remaining-requests", "5")
	ObserveRateLimit(2, h2)

	got := SnapshotRateLimits()
	if len(got) != 2 {
		t.Fatalf("want 2 snapshots, got %d", len(got))
	}
	if got[0].ChannelId != 2 {
		t.Errorf("最紧张的应排第一，got channel %d", got[0].ChannelId)
	}
	if got[0].UpdatedAt == 0 {
		t.Error("UpdatedAt 未设置")
	}
}

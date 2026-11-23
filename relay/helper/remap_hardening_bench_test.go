package helper

import (
	"testing"
)

func BenchmarkRewriteUpstreamIdentity(b *testing.B) {
	payload := []byte(`{"id":"chatcmpl-9f2b","object":"chat.completion","created":1763568000,"model":"deepseek-v3-250101","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`)
	b.ReportAllocs()
	for b.Loop() {
		_, _ = rewriteUpstreamIdentity(payload, "acme-pro")
	}
}

func BenchmarkRewriteSSELine(b *testing.B) {
	line := []byte(`data: {"id":"chatcmpl-9f2b","object":"chat.completion.chunk","model":"deepseek-v3-250101","choices":[{"index":0,"delta":{"content":"hello"}}]}` + "\n")
	b.ReportAllocs()
	for b.Loop() {
		_ = rewriteSSELine(line, "acme-pro")
	}
}

func BenchmarkRemapHardeningWriterPassthrough(b *testing.B) {
	writer, _ := newHardeningContextB(b, false, true)
	payload := []byte(`{"model":"deepseek-v3-250101","choices":[]}`)
	b.ReportAllocs()
	for b.Loop() {
		_, _ = writer.Write(payload)
	}
}

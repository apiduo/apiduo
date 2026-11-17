package service

import (
	"strings"
	"testing"
)

// 上游把整页 HTML 错误塞进任务失败原因时，客户端不该收到一整个网页；
// 但上游自己的 JSON message 要原样保留，那才是可行动的信息。
func TestSanitizeUpstreamReason(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		statusCode int
		want       string
	}{
		{
			name:       "html page becomes one sentence",
			text:       "<html>\r\n<head><title>502 Bad Gateway</title></head>\r\n<body>alb</body></html>",
			statusCode: 502,
			want:       "upstream returned an HTML error page (HTTP 502)",
		},
		{
			// 结构化错误体必须原样保留：上层的 presenter 要从中读 code。
			name: "json envelope is left verbatim",
			text: `{"error":{"code":"upstream_error","message":"content rejected"}}`,
			want: `{"error":{"code":"upstream_error","message":"content rejected"}}`,
		},
		{
			name: "whitespace collapses",
			text: "line one\r\n\tline two",
			want: "line one line two",
		},
		{
			name: "long text is truncated",
			text: strings.Repeat("x", 400),
			want: strings.Repeat("x", 300) + "…",
		},
		{
			name: "empty stays empty",
			text: "   ",
			want: "",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := SanitizeUpstreamReason(testCase.text, testCase.statusCode); got != testCase.want {
				t.Fatalf("SanitizeUpstreamReason = %q, want %q", got, testCase.want)
			}
		})
	}
}

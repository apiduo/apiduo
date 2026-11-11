package controller

import (
	"net/http"
	"testing"
)

// 200 表示上游没按 Range 切分：这时不能再对外宣称 Accept-Ranges，否则浏览器
// 会一直发 Range、拿回整包再中止（ERR_ABORTED），播放器停在「正在缓冲」。
func TestApiduoStripUnhonoredRangeHeaders(t *testing.T) {
	full := http.Header{}
	full.Set("Accept-Ranges", "bytes")
	full.Set("Content-Range", "bytes 0-1/2")
	full.Set("Content-Type", "video/mp4")

	apiduoStripUnhonoredRangeHeaders(full, http.StatusOK)
	if got := full.Get("Accept-Ranges"); got != "" {
		t.Fatalf("a 200 reply must not advertise Accept-Ranges, got %q", got)
	}
	if got := full.Get("Content-Range"); got != "" {
		t.Fatalf("a 200 reply must not carry Content-Range, got %q", got)
	}
	if got := full.Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("content type = %q", got)
	}

	partial := http.Header{}
	partial.Set("Accept-Ranges", "bytes")
	partial.Set("Content-Range", "bytes 0-1/2")
	apiduoStripUnhonoredRangeHeaders(partial, http.StatusPartialContent)
	if got := partial.Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("a 206 reply keeps Accept-Ranges, got %q", got)
	}
	if got := partial.Get("Content-Range"); got != "bytes 0-1/2" {
		t.Fatalf("a 206 reply keeps Content-Range, got %q", got)
	}
}

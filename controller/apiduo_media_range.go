package controller

// APIDUO:media-range
//
// 上游回 200 说明它没按 Range 切分（反代链路里很常见：直连容器是 206，
// 经 nginx 回来就成了整包）。这时把上游的 Accept-Ranges 一起转给浏览器，
// 浏览器就会不断重发 Range、拿回整包再中止（net::ERR_ABORTED），播放器
// 永远停在「正在缓冲」。只在该说的时候说支持分段。

import "net/http"

// apiduoStripUnhonoredRangeHeaders drops range claims from a reply that is not
// a real partial response.
func apiduoStripUnhonoredRangeHeaders(header http.Header, statusCode int) {
	if statusCode == http.StatusPartialContent || statusCode == http.StatusRequestedRangeNotSatisfiable {
		return
	}
	header.Del("Accept-Ranges")
	header.Del("Content-Range")
}

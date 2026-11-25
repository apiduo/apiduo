package advancedcustom

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 16 张 1MB 垫图走完整下载路径（SSRF 校验、拨号、重试判定、顺序写回）。
func BenchmarkResolveImageRefsConcurrent(b *testing.B) {
	payload := make([]byte, 1<<20)
	copy(payload, protocolTestPNG)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	original := imageRefAllowAddress
	imageRefAllowAddress = func(net.IP) error { return nil }
	defer func() { imageRefAllowAddress = original }()

	refs := make([]string, imageRefDownloadWorkers*2)
	for i := range refs {
		refs[i] = fmt.Sprintf("%s/a.png?i=%d", server.URL, i)
	}

	b.SetBytes(int64(len(payload) * len(refs)))
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		if _, err := resolveImageRefs(context.Background(), refs); err != nil {
			b.Fatal(err)
		}
	}
}

// 8 张 4MB 的图编码成 multipart：验证 buf.Grow 之后的分配次数。
func BenchmarkEncodeImageForm(b *testing.B) {
	payload := make([]byte, 4<<20)
	copy(payload, protocolTestPNG)
	images := make([]imageBlob, imageRefDownloadWorkers)
	for i := range images {
		images[i] = imageBlob{Data: payload, MimeType: "image/png", Name: "image"}
	}
	canonical, err := canonicalFromJSON([]byte(`{"model":"m","prompt":"p"}`))
	if err != nil {
		b.Fatal(err)
	}

	b.SetBytes(int64(len(payload) * len(images)))
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		if _, _, err := encodeImageForm(canonical, images, nil); err != nil {
			b.Fatal(err)
		}
	}
}

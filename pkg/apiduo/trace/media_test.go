package trace

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func resetMediaState() {
	mediaConsecutiveFails.Store(0)
	mediaDisabledUntil.Store(0)
	MediaCacheReset()
}

// noisePNG builds an incompressible picture: a flat colour would compress to a
// few hundred bytes and fall under the externalization threshold, which is the
// opposite of what these tests need.
func noisePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	seed := uint32(1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			seed = seed*1664525 + 1013904223
			img.SetRGBA(x, y, color.RGBA{uint8(seed), uint8(seed >> 8), uint8(seed >> 16), 0xFF})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func b64sha(raw []byte) string {
	sum := sha256.Sum256(raw)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// mediaServer emulates the three Langfuse endpoints the uploader talks to.
type mediaServer struct {
	*httptest.Server
	failCreate atomic.Bool
	creates    atomic.Int32
	puts       atomic.Int32
	patches    atomic.Int32
	wrongPut   atomic.Bool
	mediaID    string
}

func newMediaServer(t *testing.T) *mediaServer {
	t.Helper()
	ms := &mediaServer{mediaID: "abc123xyz"}
	ms.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/public/media":
			ms.creates.Add(1)
			if ms.failCreate.Load() {
				http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["traceId"] == nil || body["contentType"] == nil || body["contentLength"] == nil {
				http.Error(w, "missing fields", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"mediaId":   ms.mediaID,
				"uploadUrl": "http://" + r.Host + "/upload/" + ms.mediaID,
			})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/upload/"):
			ms.puts.Add(1)
			if r.Header.Get("x-amz-checksum-sha256") == "" || r.Header.Get("Content-Type") == "" {
				ms.wrongPut.Store(true)
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/public/media/"):
			ms.patches.Add(1)
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["uploadHttpStatus"] != float64(200) || body["uploadedAt"] == nil {
				http.Error(w, "bad confirm", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ms.Close)
	return ms
}

func mediaConf(url string) *Config {
	return &Config{
		Host:         url,
		PublicKey:    "pk-lf-test",
		SecretKey:    "sk-lf-test",
		MediaBaseURL: "https://apiduo.cn",
	}
}

func TestMediaRewriteReplacesJSONBase64(t *testing.T) {
	resetMediaState()
	srv := newMediaServer(t)
	raw := noisePNG(t, 120, 120)
	if len(raw) < mediaMinB64Chars*3/4 {
		t.Fatalf("test image too small: %d bytes", len(raw))
	}
	b64 := base64.StdEncoding.EncodeToString(raw)

	up := newMediaUploader(mediaConf(srv.URL))
	if up == nil {
		t.Fatal("uploader must be enabled when MediaBaseURL and keys are set")
	}
	in := fmt.Sprintf(`{"created":1,"data":[{"b64_json":%q}]}`, b64)
	out := up.rewrite(in, strings.Repeat("a", 32), "output")

	want := "https://apiduo.cn/api/apiduo/trace/media/" + srv.mediaID + ".png"
	if !strings.Contains(out, want) {
		t.Fatalf("output missing media url: %s", clipRunes(out, 240))
	}
	if strings.Contains(out, b64[:64]) {
		t.Fatal("base64 payload still present after rewrite")
	}
	if srv.creates.Load() != 1 || srv.puts.Load() != 1 || srv.patches.Load() != 1 {
		t.Fatalf("handshake calls = create %d, put %d, patch %d",
			srv.creates.Load(), srv.puts.Load(), srv.patches.Load())
	}
	if srv.wrongPut.Load() {
		t.Fatal("PUT must carry content-type and x-amz-checksum-sha256")
	}
}

func TestMediaRewriteHandlesDataURI(t *testing.T) {
	resetMediaState()
	srv := newMediaServer(t)
	raw := noisePNG(t, 90, 90)
	b64 := base64.StdEncoding.EncodeToString(raw)

	up := newMediaUploader(mediaConf(srv.URL))
	in := fmt.Sprintf(`{"image_url":{"url":"data:image/png;base64,%s"}}`, b64)
	out := up.rewrite(in, strings.Repeat("b", 32), "input")

	if !strings.Contains(out, srv.mediaID+".png") {
		t.Fatalf("data uri not externalized: %s", clipRunes(out, 240))
	}
	if strings.Contains(out, "data:image/png;base64,") {
		t.Fatal("data uri prefix survived")
	}
}

func TestMediaRewriteFallsBackOnFailure(t *testing.T) {
	resetMediaState()
	srv := newMediaServer(t)
	srv.failCreate.Store(true)
	raw := noisePNG(t, 90, 90)
	b64 := base64.StdEncoding.EncodeToString(raw)

	up := newMediaUploader(mediaConf(srv.URL))
	in := fmt.Sprintf(`{"b64_json":%q}`, b64)
	out := up.rewrite(in, strings.Repeat("c", 32), "output")

	if out != in {
		t.Fatal("failed upload must leave the payload untouched")
	}
}

func TestMediaRewriteIgnoresNonImages(t *testing.T) {
	resetMediaState()
	srv := newMediaServer(t)
	// 3 KB of text, base64-encoded: long enough to match the pattern, but the
	// magic-byte check must reject it.
	text := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("hello apiduo "), 400))
	up := newMediaUploader(mediaConf(srv.URL))
	in := fmt.Sprintf(`{"prompt_b64":%q}`, text)
	out := up.rewrite(in, strings.Repeat("d", 32), "output")

	if out != in {
		t.Fatal("non-image base64 must not be uploaded")
	}
	if srv.creates.Load() != 0 {
		t.Fatalf("create called %d times for a non-image", srv.creates.Load())
	}
}

func TestMediaRewriteCapsPerSpan(t *testing.T) {
	resetMediaState()
	srv := newMediaServer(t)
	raw := noisePNG(t, 80, 80)
	b64 := base64.StdEncoding.EncodeToString(raw)
	up := newMediaUploader(mediaConf(srv.URL))

	var in strings.Builder
	in.WriteString(`{"data":[`)
	for i := 0; i < 5; i++ {
		if i > 0 {
			in.WriteString(",")
		}
		fmt.Fprintf(&in, `{"b64_json":%q}`, b64)
	}
	in.WriteString(`]}`)
	out := up.rewrite(in.String(), strings.Repeat("e", 32), "output")

	if got := strings.Count(out, "/api/apiduo/trace/media/"); got != mediaMaxPerSpan {
		t.Fatalf("externalized %d images, want %d", got, mediaMaxPerSpan)
	}
}

func TestMediaUploaderDisabledWithoutBaseURL(t *testing.T) {
	resetMediaState()
	if up := newMediaUploader(&Config{Host: "http://x", PublicKey: "p"}); up != nil {
		t.Fatal("empty MediaBaseURL must disable externalization")
	}
	if up := newMediaUploader(nil); up != nil {
		t.Fatal("nil config must disable externalization")
	}
}

func TestBuildExportBodyWithMediaKeepsSpanValid(t *testing.T) {
	resetMediaState()
	srv := newMediaServer(t)
	raw := noisePNG(t, 100, 100)
	b64 := base64.StdEncoding.EncodeToString(raw)
	t.Setenv("LANGFUSE_MEDIA_BASE_URL", "")

	conf := mediaConf(srv.URL)
	sn := &Snapshot{
		TraceID: strings.Repeat("f", 32),
		SpanID:  strings.Repeat("1", 16),
		Name:    "chat flux",
		Model:   "flux-3-image",
		Success: true,
		Output:  []byte(fmt.Sprintf(`{"data":[{"b64_json":%q}]}`, b64)),
	}
	body := buildExportBody(conf, []*Snapshot{sn}, newMediaUploader(conf))
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("payload is not valid JSON after rewrite: %v", err)
	}
	if !strings.Contains(string(body), srv.mediaID+".png") {
		t.Fatal("expected the media url inside the exported payload")
	}
}

func TestParseMediaParam(t *testing.T) {
	cases := []struct {
		in string
		id string
		ok bool
	}{
		{"abc123xyz.png", "abc123xyz", true},
		{"abc123xyz", "abc123xyz", true},
		{"abc123xyz.jpeg", "abc123xyz", true},
		// The write path emits "jpg", not "jpeg": a JPEG-only case list let
		// every JPEG response 404 for as long as the feature existed.
		{"abc123xyz.jpg", "abc123xyz", true},
		{"abc123xyz.webp", "abc123xyz", true},
		{"abc123xyz.gif", "abc123xyz", true},
		{"abc123xyz.JPG", "abc123xyz", true},
		{"abc123xyz.txt", "abc123xyz.txt", false},
		{"ab.png", "ab.png", false},
		{"../../etc/passwd", "../../etc/passwd", false},
	}
	for _, tc := range cases {
		id, ok := ParseMediaParam(tc.in)
		if ok != tc.ok || (ok && id != tc.id) {
			t.Fatalf("ParseMediaParam(%q) = %q,%v want %q,%v", tc.in, id, ok, tc.id, tc.ok)
		}
	}
}

func TestSniffImage(t *testing.T) {
	png := noisePNG(t, 8, 8)
	mime, ext, ok := sniffImage(png)
	if !ok || mime != "image/png" || ext != "png" {
		t.Fatalf("png sniff = %q,%q,%v", mime, ext, ok)
	}
	if _, _, ok := sniffImage([]byte("not an image at all")); ok {
		t.Fatal("text must not sniff as an image")
	}
}

func TestThumbnailDownscales(t *testing.T) {
	raw := noisePNG(t, 800, 600)
	thumb, mime := thumbnail(raw, "image/png", 512)
	if mime != "image/jpeg" {
		t.Fatalf("thumb mime = %s", mime)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(thumb))
	if err != nil {
		t.Fatalf("thumb not decodable: %v", err)
	}
	if cfg.Width != 512 || cfg.Height != 384 {
		t.Fatalf("thumb size = %dx%d", cfg.Width, cfg.Height)
	}
	if len(thumb) >= len(raw) {
		t.Fatalf("thumb (%d) not smaller than source (%d)", len(thumb), len(raw))
	}
}

// The full chain for an image response that blows past the cap: lifted at
// capture time, restored on the export path, uploaded, and replaced by a URL.
// Before the lift existed this exact input produced 2 MB of unterminated
// base64 and no picture, because the externalizer needs a closed literal.
func TestOverCapImageSurvivesCaptureAndExport(t *testing.T) {
	resetMediaState()
	ms := newMediaServer(t)
	png := noisePNG(t, 1024, 1024)
	raw := []byte(`{"created":1,"data":[{"b64_json":"` +
		base64.StdEncoding.EncodeToString(png) + `"}]}`)

	conf := &Config{
		BodyMode:     BodyFull,
		FullMaxBytes: MinBodyFullBytes,
		Host:         ms.URL,
		PublicKey:    "pk",
		SecretKey:    "sk",
		MediaBaseURL: "https://apiduo.cn",
	}
	if len(raw) <= conf.BodyMaxBytes() {
		t.Fatalf("fixture must exceed the cap: %d bytes", len(raw))
	}

	col := &Collector{frameMode: 2}
	captured := col.cacheFullBody(raw, conf)
	if len(col.inlineMedia) != 1 {
		t.Fatalf("lifted %d pictures, want 1", len(col.inlineMedia))
	}
	if len(captured) > conf.BodyMaxBytes() {
		t.Fatalf("captured %d bytes, over the cap", len(captured))
	}

	now := time.Now()
	sn := &Snapshot{
		TraceID:        strings.Repeat("a", 32),
		Name:           "chat flux-3-image",
		Model:          "flux-3-image",
		Format:         "openai_image",
		Success:        true,
		StartUnixNano:  now.Add(-time.Second).UnixNano(),
		EndUnixNano:    now.UnixNano(),
		LatencyMs:      1000,
		Output:         captured,
		InlineMedia:    col.inlineMedia,
		HasContent:     true,
		ContentSampled: true,
	}

	body := BuildExportBodyWithMedia(conf, []*Snapshot{sn})

	if !bytes.Contains(body, []byte("https://apiduo.cn/api/apiduo/trace/media/"+ms.mediaID)) {
		t.Errorf("exported span carries no media URL: %s", string(body[:min(len(body), 240)]))
	}
	if bytes.Contains(body, []byte(fmt.Sprintf(inlineMediaPlaceholder, 0))) {
		t.Error("placeholder leaked into the export")
	}
	if ms.puts.Load() == 0 || ms.patches.Load() == 0 {
		t.Errorf("upload incomplete: puts=%d patches=%d", ms.puts.Load(), ms.patches.Load())
	}
}

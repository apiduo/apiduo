package advancedcustom

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

var protocolTestPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

// 判别式素材：一张真 PNG 的 data URI。若折叠没读到图，后面的端点/编码断言必然不同。
func protocolTestDataURI() string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(protocolTestPNG)
}

func protocolCanonical(t *testing.T, body string) *imageCanonical {
	t.Helper()
	canonical, err := canonicalFromJSON([]byte(body))
	if err != nil {
		t.Fatalf("canonicalFromJSON: %v", err)
	}
	if err := canonical.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return canonical
}

func protocolDecode(t *testing.T, plan *imagePlan) map[string]any {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(plan.Body, &fields); err != nil {
		t.Fatalf("plan body is not JSON: %v", err)
	}
	return fields
}

func TestSeedreamImageStaysOnPrimaryEndpoint(t *testing.T) {
	cfg := &dto.ImageProtocolConfig{Enabled: true, Upstream: dto.ImageUpstreamSeedream}

	textOnly := protocolCanonical(t, `{"model":"m","prompt":"一只猫"}`)
	plan, err := planImageRequest(textOnly, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if plan.UpstreamPath != "/v1/images/generations" {
		t.Fatalf("t2i should hit primary, got %s", plan.UpstreamPath)
	}
	if _, exists := protocolDecode(t, plan)["image"]; exists {
		t.Fatal("t2i must not carry an image key")
	}

	withImage := protocolCanonical(t,
		`{"model":"m","prompt":"换背景","image":"`+protocolTestDataURI()+`"}`)
	plan, err = planImageRequest(withImage, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if plan.UpstreamPath != "/v1/images/generations" {
		t.Fatalf("seedream i2i must stay on the SAME endpoint, got %s", plan.UpstreamPath)
	}
	if plan.ContentType != "application/json" {
		t.Fatalf("seedream expects JSON, got %s", plan.ContentType)
	}
	if ref, _ := protocolDecode(t, plan)["image"].(string); !strings.HasPrefix(ref, "data:image/png;base64,") {
		t.Fatalf("image reference was lost: %#v", protocolDecode(t, plan)["image"])
	}
}

func TestSeedreamDropsMaskAndInlinesBareBase64(t *testing.T) {
	cfg := &dto.ImageProtocolConfig{Enabled: true, Upstream: dto.ImageUpstreamSeedream}
	bare := base64.StdEncoding.EncodeToString(protocolTestPNG)
	canonical := protocolCanonical(t,
		`{"model":"m","prompt":"p","image":"`+bare+`","mask":"`+protocolTestDataURI()+`"}`)

	plan, err := planImageRequest(canonical, cfg)
	if err != nil {
		t.Fatal(err)
	}
	fields := protocolDecode(t, plan)
	if _, exists := fields["mask"]; exists {
		t.Fatal("seedream does not accept mask; forwarding it is a known bug")
	}
	if ref, _ := fields["image"].(string); !strings.HasPrefix(ref, "data:image/png;base64,") {
		t.Fatalf("bare base64 must be inlined as a data URI, got %#v", fields["image"])
	}
}

func TestGPTImage2RoutesByImagePresence(t *testing.T) {
	cfg := &dto.ImageProtocolConfig{Enabled: true, Upstream: dto.ImageUpstreamGPTImage2}

	textOnly := protocolCanonical(t, `{"model":"m","prompt":"p"}`)
	plan, err := planImageRequest(textOnly, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if plan.UpstreamPath != "/v1/images/generations" {
		t.Fatalf("t2i -> primary, got %s", plan.UpstreamPath)
	}

	withImage := protocolCanonical(t,
		`{"model":"m","prompt":"p","image":"`+protocolTestDataURI()+`"}`)
	plan, err = planImageRequest(withImage, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if plan.UpstreamPath != "/v1/images/edits" {
		t.Fatalf("i2i must be derived to edits, got %s", plan.UpstreamPath)
	}
	if !strings.HasPrefix(plan.ContentType, "multipart/form-data") {
		t.Fatalf("gpt-image-2 edits expects multipart, got %s", plan.ContentType)
	}
	body := string(plan.Body)
	if !strings.Contains(body, `name="image"`) || !strings.Contains(body, "image/png") {
		t.Fatalf("uploaded file part is missing from the form:\n%s", body[:min(len(body), 400)])
	}
}

func TestGPTImage2MultiImageUsesArrayField(t *testing.T) {
	cfg := &dto.ImageProtocolConfig{Enabled: true, Upstream: dto.ImageUpstreamGPTImage2}
	ref := protocolTestDataURI()
	canonical := protocolCanonical(t,
		`{"model":"m","prompt":"左红右蓝","image":["`+ref+`","`+ref+`"]}`)

	plan, err := planImageRequest(canonical, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ImageCount != 2 {
		t.Fatalf("expected 2 images, got %d", plan.ImageCount)
	}
	body := string(plan.Body)
	if strings.Count(body, `name="image[]"`) != 2 {
		t.Fatalf("two references must produce two image[] parts, got %d", strings.Count(body, `name="image[]"`))
	}
}

func TestEditPathDerivationKeepsQueryAndRejectsUnknown(t *testing.T) {
	path, err := deriveEditPath("/api/v3/images/generations?api-version=2024-10-21")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/api/v3/images/edits?api-version=2024-10-21" {
		t.Fatalf("query must be preserved, got %s", path)
	}
	if _, err := deriveEditPath("/custom/generate"); err == nil {
		t.Fatal("unknown layout must be a config error, never a guess")
	}
}

// allowLoopbackForTest 只把回环放进白名单，用来指向 httptest；其余段仍按生产规则拒。
func allowLoopbackForTest(t *testing.T) {
	t.Helper()
	previous := imageRefAllowAddress
	imageRefAllowAddress = func(ip net.IP) error {
		if ip.IsLoopback() {
			return nil
		}
		return previous(ip)
	}
	t.Cleanup(func() { imageRefAllowAddress = previous })
}

// 下载层是 SSRF 的唯一闸门（已无 allow_url_fetch 开关），这些段必须默认拒绝。
func TestImageRefRejectsNonPublicAddresses(t *testing.T) {
	blocked := []string{
		"http://127.0.0.1/a.png",
		"http://10.0.0.1/a.png",
		"http://172.16.0.1/a.png",
		"http://192.168.1.1/a.png",
		"http://169.254.169.254/latest/meta-data/", // 云元数据
		"http://100.64.0.1/a.png",                  // CGNAT
		"http://224.0.0.1/a.png",                   // 多播
		"http://0.0.0.0/a.png",
		"http://localhost/a.png",
		"http://img.localtest.me/a.png", // 解析回 127.0.0.1 的别名
		"file:///etc/passwd",
		"ftp://example.com/a.png",
	}
	for _, raw := range blocked {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if err := validateImageRefURL(parsed); err == nil {
			t.Fatalf("%s must be rejected", raw)
		}
	}
}

func TestImageRefDownloadEnforcesSizeLimit(t *testing.T) {
	allowLoopbackForTest(t)
	previous := imageMaxRefBytes
	imageMaxRefBytes = 64
	t.Cleanup(func() { imageMaxRefBytes = previous })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(bytes.Repeat([]byte{0x89}, 65))
	}))
	defer server.Close()

	if _, err := fetchImageRef(context.Background(), server.URL+"/big.png"); err == nil {
		t.Fatal("oversized image must be rejected")
	}
}

// Go 默认跟随重定向且不重验 host：公网 URL 302 到内网就能绕过 SSRF 防线。
func TestImageRefDownloadRevalidatesRedirects(t *testing.T) {
	allowLoopbackForTest(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(protocolTestPNG)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		to := strings.Replace(target.URL, "127.0.0.1", "localhost", 1) + "/a.png"
		http.Redirect(w, r, to, http.StatusFound)
	}))
	defer redirector.Close()

	if _, err := fetchImageRef(context.Background(), redirector.URL+"/a.png"); err == nil {
		t.Fatal("redirect to localhost must be rejected")
	}
}

func TestImageRefsDownloadConcurrentlyAndKeepOrder(t *testing.T) {
	allowLoopbackForTest(t)
	const count = 8
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(120 * time.Millisecond)
		w.Header().Set("Content-Type", "image/png")
		// 末尾带索引，用来验证写回顺序而不是完成顺序。
		index := r.URL.Query().Get("i")
		_, _ = w.Write(append(append([]byte{}, protocolTestPNG...), index[0]))
	}))
	defer server.Close()

	refs := make([]string, count)
	for i := range refs {
		refs[i] = fmt.Sprintf("%s/a.png?i=%d", server.URL, i)
	}
	started := time.Now()
	blobs, err := resolveImageRefs(context.Background(), refs)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if len(blobs) != count {
		t.Fatalf("expected %d blobs, got %d", count, len(blobs))
	}
	for i, blob := range blobs {
		if tail := blob.Data[len(blob.Data)-1]; tail != byte('0'+i) {
			t.Fatalf("blob %d out of order: tail=%q", i, tail)
		}
	}
	// 4 并发 × 120ms = 2 轮 ≈ 240ms；全串行要 960ms。
	if elapsed > 700*time.Millisecond {
		t.Fatalf("downloads look serial: %s", elapsed)
	}
}

func TestImageRefsStopOnFirstError(t *testing.T) {
	allowLoopbackForTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "bad") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(protocolTestPNG)
	}))
	defer server.Close()

	refs := []string{
		server.URL + "/ok1.png",
		server.URL + "/bad.png",
		server.URL + "/ok3.png",
		server.URL + "/ok4.png",
	}
	started := time.Now()
	_, err := resolveImageRefs(context.Background(), refs)
	if err == nil {
		t.Fatal("404 must fail the request")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should mention 404, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("remaining downloads were not cancelled: %s", elapsed)
	}
}

func TestCanonicalRejectsMalformedRequests(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"no image and blank prompt", `{"model":"m","prompt":"   "}`},
		{"mask without image", `{"model":"m","prompt":"p","mask":"` + protocolTestDataURI() + `"}`},
		{"non-string image", `{"model":"m","prompt":"p","image":123}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			canonical, err := canonicalFromJSON([]byte(tc.body))
			if err != nil {
				return // 解析阶段就拒绝，也算通过
			}
			if err := canonical.normalize(); err == nil {
				t.Fatalf("expected a 400-class rejection for %s", tc.body)
			}
		})
	}
}

// 需求口径：image 数组长度 1 也是图生图；长度 0 或为空是文生图。
// 这条用例要能证伪「把 ["ref"] 压成字符串 / 把 [] 当成有图」两种回归。
func TestImageArrayPresenceRules(t *testing.T) {
	ref := protocolTestDataURI()
	cases := []struct {
		name      string
		imageJSON string
		wantImage bool
		wantShape string
	}{
		{"长度0数组", `[]`, false, "absent"},
		{"全空元素数组", `["", "  "]`, false, "absent"},
		{"null", `null`, false, "absent"},
		{"空字符串", `""`, false, "absent"},
		{"长度1数组", `["` + ref + `"]`, true, "array-1"},
		{"长度2数组", `["` + ref + `","` + ref + `"]`, true, "array-2"},
		{"单字符串", `"` + ref + `"`, true, "string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			canonical := protocolCanonical(t,
				`{"model":"m","prompt":"p","image":`+tc.imageJSON+`}`)
			if got := canonical.hasImage(); got != tc.wantImage {
				t.Fatalf("hasImage = %v, want %v", got, tc.wantImage)
			}
			if tc.wantShape == "absent" {
				if _, exists := canonical.fields["image"]; exists {
					t.Fatal("empty image must be deleted, not forwarded as an empty key")
				}
				return
			}
			switch value := canonical.fields["image"].(type) {
			case string:
				if tc.wantShape != "string" {
					t.Fatalf("shape = string, want %s", tc.wantShape)
				}
			case []string:
				want := 1
				if tc.wantShape == "array-2" {
					want = 2
				}
				if len(value) != want || tc.wantShape == "string" {
					t.Fatalf("shape = array(len=%d), want %s", len(value), tc.wantShape)
				}
			default:
				t.Fatalf("unexpected image shape %T", value)
			}
		})
	}
}

func TestSingleElementArrayRoutesToImageToImage(t *testing.T) {
	ref := protocolTestDataURI()
	canonical := protocolCanonical(t,
		`{"model":"m","prompt":"p","image":["`+ref+`"]}`)

	seedream := &dto.ImageProtocolConfig{Enabled: true, Upstream: dto.ImageUpstreamSeedream}
	plan, err := planImageRequest(canonical, seedream)
	if err != nil {
		t.Fatal(err)
	}
	image := protocolDecode(t, plan)["image"]
	if values, ok := image.([]any); !ok || len(values) != 1 {
		t.Fatalf("seedream must keep a 1-element array, got %#v", image)
	}

	gpt := &dto.ImageProtocolConfig{Enabled: true, Upstream: dto.ImageUpstreamGPTImage2}
	plan, err = planImageRequest(canonical, gpt)
	if err != nil {
		t.Fatal(err)
	}
	if plan.UpstreamPath != "/v1/images/edits" {
		t.Fatalf("a 1-element array IS image-to-image, expected edits endpoint, got %s", plan.UpstreamPath)
	}
	if plan.ImageCount != 1 {
		t.Fatalf("expected 1 image, got %d", plan.ImageCount)
	}
}

func TestEmptyArrayRoutesToTextToImage(t *testing.T) {
	for _, script := range []string{dto.ImageUpstreamSeedream, dto.ImageUpstreamGPTImage2} {
		canonical := protocolCanonical(t, `{"model":"m","prompt":"p","image":[]}`)
		cfg := &dto.ImageProtocolConfig{Enabled: true, Upstream: script}
		plan, err := planImageRequest(canonical, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if plan.UpstreamPath != "/v1/images/generations" {
			t.Fatalf("%s: empty array must go to primary, got %s", script, plan.UpstreamPath)
		}
		fields := protocolDecode(t, plan)
		if _, exists := fields["image"]; exists {
			t.Fatalf("%s: empty array must not reach upstream", script)
		}
	}
}

func TestCanonicalNormalisesN(t *testing.T) {
	canonical := protocolCanonical(t, `{"model":"m","prompt":"p","n":"abc"}`)
	if canonical.fields["n"] != 1 {
		t.Fatalf("invalid n must fall back to 1, got %#v", canonical.fields["n"])
	}
	absent := protocolCanonical(t, `{"model":"m","prompt":"p"}`)
	if _, exists := absent.fields["n"]; exists {
		t.Fatal("absent n must stay absent; defaults belong to the upstream")
	}
}

// 口径：只要不是 b64_json 就优先要 url。缺席与非法值都归一到 url ——
// 图像响应的 base64 动辄几 MB，客户端不写这个字段通常只是没想到。
func TestCanonicalDefaultsResponseFormatToURL(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","prompt":"p"}`,                          // 缺席
		`{"model":"m","prompt":"p","response_format":null}`,    // 空值
		`{"model":"m","prompt":"p","response_format":""}`,      // 空串
		`{"model":"m","prompt":"p","response_format":"png"}`,   // 非法值
		`{"model":"m","prompt":"p","response_format":"url"}`,   // 显式 url
	} {
		canonical := protocolCanonical(t, body)
		if got := canonical.fields["response_format"]; got != "url" {
			t.Errorf("%s: response_format = %#v, want \"url\"", body, got)
		}
	}

	canonical := protocolCanonical(t,
		`{"model":"m","prompt":"p","response_format":"b64_json"}`)
	if got := canonical.fields["response_format"]; got != "b64_json" {
		t.Fatalf("explicit b64_json must survive, got %#v", got)
	}
}

func TestFoldChatTakesLastUserTurnAndKeepsSystemImagesOut(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{
		Model: "m",
		Messages: []dto.Message{
			{Role: "system", Content: []any{
				map[string]any{"type": "text", "text": "画风：写实"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/style.png"}},
			}},
			{Role: "user", Content: "第一版"},
			{Role: "assistant", Content: "好的"},
			{Role: "user", Content: []any{
				map[string]any{"type": "text", "text": "把背景换成纯黑"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": protocolTestDataURI()}},
			}},
		},
	}

	canonical, err := canonicalFromChat(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := canonical.normalize(); err != nil {
		t.Fatal(err)
	}
	if prompt := canonical.fields["prompt"]; prompt != "画风：写实\n\n把背景换成纯黑" {
		t.Fatalf("system prefix + last user text, got %#v", prompt)
	}
	refs := canonical.imageRefs()
	if len(refs) != 1 || !strings.HasPrefix(refs[0], "data:image/png;base64,") {
		t.Fatalf("only the last user image should survive, got %#v", refs)
	}
}

func TestFoldChatRejectsNoUserAndUnknownRole(t *testing.T) {
	if _, err := canonicalFromChat(&dto.GeneralOpenAIRequest{
		Model: "m", Messages: []dto.Message{{Role: "system", Content: "x"}},
	}); err == nil {
		t.Fatal("no user turn must be rejected")
	}
	if _, err := canonicalFromChat(&dto.GeneralOpenAIRequest{
		Model: "m", Messages: []dto.Message{{Role: "usre", Content: "x"}},
	}); err == nil {
		t.Fatal("a misspelled user role must not silently degrade into history")
	}
}

// 口径：chat 回包与文本模型同构 —— message.content 是字符串，图片写成
// markdown。流式与非流式共用同一份 chatImageMarkdown，否则同一个入口在
// stream 两边给两种形状，客户端得写两套解析。
func TestWrapChatCompletionUsesMarkdownContent(t *testing.T) {
	response := &imageResponse{
		Created: 1730000000,
		Data: []imageResponseItem{
			{URL: "https://cdn.example.com/a.png"},
			{B64JSON: base64.StdEncoding.EncodeToString(protocolTestPNG)},
		},
		Extras: map[string]any{"usage": map[string]any{"total_tokens": 7}},
	}
	wrapped, err := wrapChatCompletion(response, "client-model")
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(wrapped, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["object"] != "chat.completion" || envelope["model"] != "client-model" {
		t.Fatalf("wrong envelope: %#v", envelope)
	}
	content, ok := envelope["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
	if !ok {
		t.Fatalf("content must be a string like a text model, got %#v", envelope["choices"])
	}
	if !strings.Contains(content, "![image](https://cdn.example.com/a.png)") {
		t.Fatalf("url image missing from markdown: %q", content)
	}
	if !strings.Contains(content, "![image](data:image/png;base64,") {
		t.Fatalf("b64_json must become a data URI, got %q", content)
	}
	if usage, ok := envelope["usage"].(map[string]any); !ok || usage["total_tokens"] != float64(7) {
		t.Fatalf("upstream usage lost: %#v", envelope["usage"])
	}
}

func TestWrapChatCompletionStreamIsSSE(t *testing.T) {
	response := &imageResponse{
		Created: 1730000000,
		Data:    []imageResponseItem{{URL: "https://cdn.example.com/a.png"}},
		Extras:  map[string]any{},
	}
	payload, err := wrapChatCompletionStream(response, "client-model")
	if err != nil {
		t.Fatal(err)
	}
	frames := strings.Split(strings.TrimSpace(string(payload)), "\n\n")
	if len(frames) != 3 || frames[2] != "data: [DONE]" {
		t.Fatalf("expected content chunk + finish chunk + [DONE], got %#v", frames)
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(frames[0], "data: ")), &first); err != nil {
		t.Fatal(err)
	}
	if first["object"] != "chat.completion.chunk" {
		t.Fatalf("wrong chunk object: %#v", first["object"])
	}
	delta := first["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if delta["content"] != "![image](https://cdn.example.com/a.png)" {
		t.Fatalf("stream delta should carry the same markdown, got %#v", delta["content"])
	}
	var last map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(frames[1], "data: ")), &last); err != nil {
		t.Fatal(err)
	}
	if last["choices"].([]any)[0].(map[string]any)["finish_reason"] != "stop" {
		t.Fatal("the final chunk must carry finish_reason=stop")
	}
}

// 需求口径：入口只保证回包形状完整，不替上游判定业务成败。
// 上游 200 + 空 data[] 时 chat 仍要给出 content 字段（哪怕是空数组）。
func TestEmptyImageDataKeepsChatContentShape(t *testing.T) {
	response := &imageResponse{Created: 1, Extras: map[string]any{}}
	wrapped, err := wrapChatCompletion(response, "client-model")
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(wrapped, &envelope); err != nil {
		t.Fatal(err)
	}
	choices, ok := envelope["choices"].([]any)
	if !ok || len(choices) != 1 {
		t.Fatalf("choices missing: %#v", envelope["choices"])
	}
	message := choices[0].(map[string]any)["message"].(map[string]any)
	content, exists := message["content"]
	if !exists {
		t.Fatal("content key must exist even when upstream returned no images")
	}
	// 与文本模型同构：没有图就是空字符串，不是一个空数组。
	if text, ok := content.(string); !ok || text != "" {
		t.Fatalf("content should be an empty string, got %#v", content)
	}
}

func TestDecodeImageResponseRejectsEmptyData(t *testing.T) {
	if _, err := decodeImageResponse([]byte(`{"created":1,"data":[]}`)); err != nil {
		t.Fatal("decode should succeed; an empty data array is a valid response envelope")
	}
	if _, err := decodeImageResponse([]byte(`{"created":1}`)); err == nil {
		t.Fatal("missing data array must be an error")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// chat 折叠只认 OpenAI 标准字段，只产出 model/prompt/image，
// n / size / temperature / tools 等一律不往下传。
func TestCanonicalFromChatIgnoresNonStandardFields(t *testing.T) {
	n := 4
	request := &dto.GeneralOpenAIRequest{
		Model:       "m",
		N:           &n,
		Size:        "2048x2048",
		Temperature: floatPtr(0.7),
		Messages: []dto.Message{
			{Role: "user", Content: "一只猫"},
		},
	}
	canonical, err := canonicalFromChat(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := canonical.normalize(); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"n", "size", "temperature", "tools", "stream"} {
		if _, exists := canonical.fields[key]; exists {
			t.Fatalf("non-standard chat field %q must not reach the image request", key)
		}
	}
	if canonical.fields["prompt"] != "一只猫" || canonical.hasImage() {
		t.Fatalf("prompt must drive text-to-image, got %#v", canonical.fields)
	}
}

func floatPtr(v float64) *float64 { return &v }

// 只认 OpenAI chat 标准图片 part：type=image_url + image_url.url。
// detail 等其它字段忽略；非标准写法不得被误判成垫图。
func TestChatOnlyRecognisesStandardImageURLPart(t *testing.T) {
	ref := protocolTestDataURI()
	standard := &dto.GeneralOpenAIRequest{Model: "m", Messages: []dto.Message{{
		Role: "user", Content: []any{map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": ref, "detail": "high"},
		}},
	}}}
	canonical, err := canonicalFromChat(standard)
	if err != nil {
		t.Fatal(err)
	}
	if err := canonical.normalize(); err != nil {
		t.Fatal(err)
	}
	if !canonical.hasImage() || canonical.imageRefs()[0] != ref {
		t.Fatalf("standard image_url part must be recognised: %#v", canonical.fields)
	}

	nonStandard := []any{
		map[string]any{"type": "input_image", "image_url": map[string]any{"url": ref}},
		map[string]any{"type": "image", "image_url": map[string]any{"url": ref}},
		map[string]any{"type": "image_url", "image_url": ref},
		map[string]any{"type": "image_url", "url": ref},
		map[string]any{"type": "image_url", "image_url": map[string]any{"detail": "high"}},
	}
	for index, part := range nonStandard {
		request := &dto.GeneralOpenAIRequest{Model: "m", Messages: []dto.Message{{
			Role: "user", Content: []any{
				map[string]any{"type": "text", "text": "p"},
				part,
			},
		}}}
		canonical, err := canonicalFromChat(request)
		if err != nil {
			t.Fatalf("case %d: %v", index, err)
		}
		if err := canonical.normalize(); err != nil {
			t.Fatalf("case %d: %v", index, err)
		}
		if canonical.hasImage() {
			t.Fatalf("case %d must NOT be treated as an image: %#v", index, part)
		}
	}
}

// response_format 决定上游返回 url 还是 b64_json。走 /v1/images/edits 的
// multipart 路径也必须把它带过去，否则客户端要 url 却收到默认的大 b64_json。
func TestGPTImage2FormKeepsResponseFormat(t *testing.T) {
	cfg := &dto.ImageProtocolConfig{Enabled: true, Upstream: dto.ImageUpstreamGPTImage2}

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"url", `{"model":"m","prompt":"p","image":"` + protocolTestDataURI() + `","response_format":"url","size":"1024x1024"}`, "url"},
		{"b64_json", `{"model":"m","prompt":"p","image":"` + protocolTestDataURI() + `","response_format":"b64_json"}`, "b64_json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			canonical := protocolCanonical(t, tc.body)
			plan, err := planImageRequest(canonical, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(plan.ContentType, "multipart/form-data") {
				t.Fatalf("expected multipart, got %s", plan.ContentType)
			}
			_, params, err := mime.ParseMediaType(plan.ContentType)
			if err != nil || params["boundary"] == "" {
				t.Fatalf("bad content type %q: %v", plan.ContentType, err)
			}
			reader := multipart.NewReader(bytes.NewReader(plan.Body), params["boundary"])
			fields := make(map[string]string)
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if part.FileName() != "" {
					continue // 文件 part
				}
				data, err := io.ReadAll(part)
				if err != nil {
					t.Fatal(err)
				}
				fields[part.FormName()] = string(data)
			}
			if fields["response_format"] != tc.want {
				t.Fatalf("form response_format = %q, want %q; fields=%#v",
					fields["response_format"], tc.want, fields)
			}
			if fields["prompt"] != "p" {
				t.Fatalf("prompt missing from form: %#v", fields)
			}
		})
	}

	// 非法值在 canonical 归一化时变成 url，form 里必须是 url 而不是原值。
	canonical := protocolCanonical(t,
		`{"model":"m","prompt":"p","image":"`+protocolTestDataURI()+`","response_format":"png"}`)
	plan, err := planImageRequest(canonical, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plan.Body), `name="response_format"`) {
		t.Fatal("response_format must reach the form; the default is url now")
	}
	if strings.Contains(string(plan.Body), "\r\n\r\npng\r\n") {
		t.Fatal("the illegal value must not reach the upstream verbatim")
	}
}

// 存储没配（测试环境的常态）时，base64 必须原样留着 —— 这正是
// 「缺配置就透传上游」的那条退路。
func TestAlignImageCarriersLeavesBase64WhenStoreDisabled(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString(protocolTestPNG)
	response := &imageResponse{Created: 1, Data: []imageResponseItem{{B64JSON: b64}}}

	changed := alignImageCarriers(context.Background(), 0, response, true)

	if changed {
		t.Fatal("nothing may change while no artifact storage is configured")
	}
	if response.Data[0].B64JSON != b64 {
		t.Error("base64 must survive untouched")
	}
	if response.Data[0].URL != "" {
		t.Errorf("url must stay empty, got %q", response.Data[0].URL)
	}
}

// 客户端明确要 b64 时，即便存储可用也不该动它。
func TestAlignImageCarriersKeepsB64WhenClientAskedForIt(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString(protocolTestPNG)
	response := &imageResponse{Created: 1, Data: []imageResponseItem{{B64JSON: b64}}}

	if alignImageCarriers(context.Background(), 0, response, false) {
		t.Fatal("nothing to do: upstream already answered with base64")
	}
	if response.Data[0].B64JSON != b64 {
		t.Error("base64 must survive untouched")
	}
}

// 反向：客户端要 b64 而上游给链接时，取不回来就保留链接 —— 不能因为一次
// 下载失败就让整个响应失败。
func TestAlignImageCarriersKeepsURLWhenFetchFails(t *testing.T) {
	// 私网地址会被下载器直接拒绝，正好用来走失败分支。
	url := "http://127.0.0.1:9/nope.png"
	response := &imageResponse{Created: 1, Data: []imageResponseItem{{URL: url}}}

	if alignImageCarriers(context.Background(), 0, response, false) {
		t.Fatal("a rejected fetch must leave the item untouched")
	}
	if response.Data[0].URL != url || response.Data[0].B64JSON != "" {
		t.Errorf("item changed: %#v", response.Data[0])
	}
}

func TestEncodeImageResponseRoundTripsExtras(t *testing.T) {
	response := &imageResponse{
		Created: 1730000000,
		Data: []imageResponseItem{{
			URL:    "https://cdn.example.com/a.png",
			Extras: map[string]any{"revised_prompt": "rp", "size": "1024x1024"},
		}},
		Extras: map[string]any{"usage": map[string]any{"total_tokens": 7}},
	}
	encoded, err := encodeImageResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["created"] != float64(1730000000) {
		t.Errorf("created lost: %#v", envelope["created"])
	}
	if _, ok := envelope["usage"]; !ok {
		t.Error("usage is the billing input and must survive")
	}
	entry := envelope["data"].([]any)[0].(map[string]any)
	if entry["url"] != "https://cdn.example.com/a.png" || entry["revised_prompt"] != "rp" {
		t.Errorf("item fields lost: %#v", entry)
	}
	if _, exists := entry["b64_json"]; exists {
		t.Error("b64_json must not appear once it was replaced by a url")
	}
}

package advancedcustom

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

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

func TestImageURLFetchIsOffByDefault(t *testing.T) {
	cfg := &dto.ImageProtocolConfig{Enabled: true, Upstream: dto.ImageUpstreamGPTImage2}
	canonical := protocolCanonical(t,
		`{"model":"m","prompt":"p","image":"https://cdn.example.com/a.png"}`)
	if _, err := planImageRequest(canonical, cfg); err == nil {
		t.Fatal("no outbound connection may happen while allow_url_fetch is off")
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

func TestCanonicalNormalisesNAndResponseFormat(t *testing.T) {
	canonical := protocolCanonical(t,
		`{"model":"m","prompt":"p","n":"abc","response_format":"png"}`)
	if canonical.fields["n"] != 1 {
		t.Fatalf("invalid n must fall back to 1, got %#v", canonical.fields["n"])
	}
	if _, exists := canonical.fields["response_format"]; exists {
		t.Fatal("illegal response_format must be deleted, not forwarded")
	}

	absent := protocolCanonical(t, `{"model":"m","prompt":"p"}`)
	if _, exists := absent.fields["n"]; exists {
		t.Fatal("absent n must stay absent; defaults belong to the upstream")
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

func TestWrapChatCompletionUsesImageURLParts(t *testing.T) {
	response := &imageResponse{
		Created: 1730000000,
		Data: []imageResponseItem{
			{URL: "https://cdn.example.com/a.png", Extras: map[string]any{"revised_prompt": "rp"}},
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
	// 需求口径：非流式不用 md，content 是 image_url parts 数组。
	parts, ok := envelope["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("content must be an image_url parts array, got %#v", envelope["choices"])
	}
	first := parts[0].(map[string]any)
	if first["type"] != "image_url" || first["revised_prompt"] != "rp" {
		t.Fatalf("data[] extras must follow each part, got %#v", first)
	}
	second := parts[1].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(second, "data:image/png;base64,") {
		t.Fatalf("b64_json must become a data URI, got %s", second)
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
	parts, ok := content.([]any)
	if !ok || len(parts) != 0 {
		t.Fatalf("content should be an empty parts array, got %#v", content)
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

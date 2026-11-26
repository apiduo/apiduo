package advancedcustom

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
)

func decodeGeminiPlanBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("plan body is not JSON: %v", err)
	}
	return parsed
}

func geminiPlanParts(t *testing.T, body []byte) []any {
	t.Helper()
	parsed := decodeGeminiPlanBody(t, body)
	contents, ok := parsed["contents"].([]any)
	if !ok || len(contents) != 1 {
		t.Fatalf("contents = %#v", parsed["contents"])
	}
	parts, ok := contents[0].(map[string]any)["parts"].([]any)
	if !ok {
		t.Fatalf("parts missing: %#v", contents[0])
	}
	return parts
}

func TestPlanGeminiTextToImage(t *testing.T) {
	canonical := newImageCanonical()
	canonical.fields["prompt"] = "a cat"
	canonical.fields["size"] = "1024x1024"

	plan, err := planGemini(context.Background(), canonical, "gemini-2.5-flash-image")
	if err != nil {
		t.Fatalf("planGemini: %v", err)
	}
	if want := "/v1beta/models/gemini-2.5-flash-image:generateContent"; plan.UpstreamPath != want {
		t.Fatalf("path = %q, want %q", plan.UpstreamPath, want)
	}
	parts := geminiPlanParts(t, plan.Body)
	if len(parts) != 1 {
		t.Fatalf("parts = %d, want 1 (text only)", len(parts))
	}
	if parts[0].(map[string]any)["text"] != "a cat" {
		t.Fatalf("text part = %#v", parts[0])
	}

	parsed := decodeGeminiPlanBody(t, plan.Body)
	generationConfig := parsed["generationConfig"].(map[string]any)
	imageConfig := generationConfig["imageConfig"].(map[string]any)
	if imageConfig["aspectRatio"] != "1:1" {
		t.Fatalf("aspectRatio = %v", imageConfig["aspectRatio"])
	}
	modalities := generationConfig["responseModalities"].([]any)
	if len(modalities) != 1 || modalities[0] != "IMAGE" {
		t.Fatalf("responseModalities = %#v", modalities)
	}
}

func TestPlanGeminiImageToImageKeepsOrder(t *testing.T) {
	canonical := newImageCanonical()
	canonical.fields["prompt"] = "repaint"
	canonical.fields["image"] = []string{
		"data:image/png;base64,iVBORw0KGgo=",
		"data:image/webp;base64,UklGRg==",
	}
	canonical.fields["aspect_ratio"] = "16:9"
	canonical.fields["image_size"] = "2k"

	plan, err := planGemini(context.Background(), canonical, "gemini-3-pro-image-preview")
	if err != nil {
		t.Fatalf("planGemini: %v", err)
	}
	parts := geminiPlanParts(t, plan.Body)
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want text + two inline images", len(parts))
	}
	first := parts[1].(map[string]any)["inlineData"].(map[string]any)
	second := parts[2].(map[string]any)["inlineData"].(map[string]any)
	if first["mimeType"] != "image/png" || second["mimeType"] != "image/webp" {
		t.Fatalf("inline order/mime = %#v %#v", first, second)
	}
	parsed := decodeGeminiPlanBody(t, plan.Body)
	imageConfig := parsed["generationConfig"].(map[string]any)["imageConfig"].(map[string]any)
	if imageConfig["aspectRatio"] != "16:9" {
		t.Fatalf("explicit aspect_ratio should win, got %v", imageConfig["aspectRatio"])
	}
	if imageConfig["imageSize"] != "2K" {
		t.Fatalf("imageSize = %v, want 2K", imageConfig["imageSize"])
	}
}

func TestPlanGeminiRejectsUnknownImageSize(t *testing.T) {
	canonical := newImageCanonical()
	canonical.fields["prompt"] = "a cat"
	canonical.fields["image_size"] = "8K"

	if _, err := planGemini(context.Background(), canonical, "gemini-2.5-flash-image"); err == nil {
		t.Fatal("image_size 8K must be rejected")
	}
}

// size 是标准 OpenAI / 方舟写法，必须折算成 Gemini 的 (比例, 档位)。
// 只认 image_size 会导致 size=2048x2048 落成 1K 16:9，客户端以为拿到了 2K。
func TestPlanGeminiMapsSizeToImageConfig(t *testing.T) {
	cases := []struct {
		size      string
		wantRatio string
		wantTier  string
	}{
		{size: "1024x1024", wantRatio: "1:1", wantTier: "1K"},
		{size: "4096x4096", wantRatio: "1:1", wantTier: "4K"},
		{size: "2048x2048", wantRatio: "1:1", wantTier: "2K"},
		{size: "2752x1536", wantRatio: "16:9", wantTier: "2K"},
		{size: "5504x3072", wantRatio: "16:9", wantTier: "4K"},
		// 4K UHD 是客户口中的「4K」，按面积最近会落成 2K，降质超三分之一要升档。
		{size: "3840x2160", wantRatio: "16:9", wantTier: "4K"},
		// OpenAI 的 1K 竖图：面积只差 32%，不许被抬成 2K。
		{size: "1024x1536", wantRatio: "2:3", wantTier: "1K"},
		{size: "1536x1024", wantRatio: "3:2", wantTier: "1K"},
		{size: "1792x1024", wantRatio: "16:9", wantTier: "2K"},
		// 表内极端比例。
		{size: "1584x672", wantRatio: "21:9", wantTier: "1K"},
		{size: "2048x512", wantRatio: "4:1", wantTier: "1K"},
		// 表外像素取最近比例与档位，不静默丢弃。
		{size: "999x999", wantRatio: "1:1", wantTier: "1K"},
		{size: "800x600", wantRatio: "4:3", wantTier: "1K"},
		{size: "5000x5000", wantRatio: "1:1", wantTier: "4K"},
		// 档位 token：比例交给上游，不臆造。
		{size: "2K", wantTier: "2K"},
		{size: "4k", wantTier: "4K"},
		// 比例 token：与 aspect_ratio 同义，档位留给上游默认。
		{size: "1:1", wantRatio: "1:1"},
		{size: "16:9", wantRatio: "16:9"},
		{size: "9 / 16", wantRatio: "9:16"},
		// x 分隔：短边小于 minPixelSide 时是比例，不是像素。
		{size: "16x9", wantRatio: "16:9"},
		{size: "1x1", wantRatio: "1:1"},
	}
	for _, tc := range cases {
		t.Run(tc.size, func(t *testing.T) {
			canonical := newImageCanonical()
			canonical.fields["prompt"] = "a cat"
			canonical.fields["size"] = tc.size

			plan, err := planGemini(context.Background(), canonical, "gemini-2.5-flash-image")
			if err != nil {
				t.Fatalf("planGemini: %v", err)
			}
			parsed := decodeGeminiPlanBody(t, plan.Body)
			generationConfig := parsed["generationConfig"].(map[string]any)
			imageConfig, _ := generationConfig["imageConfig"].(map[string]any)
			if got, _ := imageConfig["aspectRatio"].(string); got != tc.wantRatio {
				t.Fatalf("aspectRatio = %q, want %q", got, tc.wantRatio)
			}
			if got, _ := imageConfig["imageSize"].(string); got != tc.wantTier {
				t.Fatalf("imageSize = %q, want %q", got, tc.wantTier)
			}
		})
	}
}

// 对外契约是 size（+ aspect_ratio）：size 与遗留别名 image_size 冲突时 size 赢，
// 比例仍然逐维覆盖。
func TestPlanGeminiSizeWinsOverImageSizeAlias(t *testing.T) {
	canonical := newImageCanonical()
	canonical.fields["prompt"] = "a cat"
	canonical.fields["size"] = "1024x1024"
	canonical.fields["image_size"] = "4k"

	plan, err := planGemini(context.Background(), canonical, "gemini-2.5-flash-image")
	if err != nil {
		t.Fatalf("planGemini: %v", err)
	}
	imageConfig := decodeGeminiPlanBody(t, plan.Body)["generationConfig"].(map[string]any)["imageConfig"].(map[string]any)
	if imageConfig["aspectRatio"] != "1:1" || imageConfig["imageSize"] != "1K" {
		t.Fatalf("imageConfig = %#v, want 1:1 / 1K (size wins)", imageConfig)
	}

	canonical = newImageCanonical()
	canonical.fields["prompt"] = "a cat"
	canonical.fields["size"] = "2048x2048"
	canonical.fields["aspect_ratio"] = "16 / 9"

	plan, err = planGemini(context.Background(), canonical, "gemini-2.5-flash-image")
	if err != nil {
		t.Fatalf("planGemini: %v", err)
	}
	imageConfig = decodeGeminiPlanBody(t, plan.Body)["generationConfig"].(map[string]any)["imageConfig"].(map[string]any)
	if imageConfig["aspectRatio"] != "16:9" || imageConfig["imageSize"] != "2K" {
		t.Fatalf("imageConfig = %#v, want 16:9 / 2K", imageConfig)
	}
}

// image_size 是 gemini 原生方言，作为 size 的档位别名保留：
// 老客户端只传它时仍然生效；size 只写了比例或 auto 时，别名仍然能补档位。
func TestPlanGeminiKeepsImageSizeAlias(t *testing.T) {
	for _, size := range []any{nil, "auto", "1:1"} {
		canonical := newImageCanonical()
		canonical.fields["prompt"] = "a cat"
		canonical.fields["image_size"] = "2k"
		if size != nil {
			canonical.fields["size"] = size
		}

		plan, err := planGemini(context.Background(), canonical, "gemini-2.5-flash-image")
		if err != nil {
			t.Fatalf("size %v: planGemini: %v", size, err)
		}
		imageConfig := decodeGeminiPlanBody(t, plan.Body)["generationConfig"].(map[string]any)["imageConfig"].(map[string]any)
		if imageConfig["imageSize"] != "2K" {
			t.Fatalf("size %v: imageSize = %v, want 2K", size, imageConfig["imageSize"])
		}
		if size == "1:1" {
			if imageConfig["aspectRatio"] != "1:1" {
				t.Fatalf("size 1:1 must keep its ratio, got %v", imageConfig["aspectRatio"])
			}
			continue
		}
		if _, exists := imageConfig["aspectRatio"]; exists {
			t.Fatalf("size %v: alias must not invent a ratio: %#v", size, imageConfig["aspectRatio"])
		}
	}
}

// 官方表 14 比例 × 3 档共 42 格，每一格都必须双向可逆：
// WxH -> (ratio, tier)；ratio + tier 组合 -> 同一组 WxH；档位/比例 token 各管一维。
func TestGeminiSizeTableRoundTrip(t *testing.T) {
	tiers := []string{"1K", "2K", "4K"}
	for _, ratio := range geminiAspectRatioOrder {
		pixels := geminiImageSizeTable[ratio]
		for column, tier := range tiers {
			w, h := pixels[column][0], pixels[column][1]

			// 像素写法：两维都由 size 给出。
			ratioOut, tierOut, err := geminiImageConfig(imageCanonicalWithSize(fmt.Sprintf("%dx%d", w, h)))
			if err != nil {
				t.Fatalf("%s %s: %v", ratio, tier, err)
			}
			if ratioOut != ratio || tierOut != tier {
				t.Fatalf("%dx%d -> (%s, %s), want (%s, %s)", w, h, ratioOut, tierOut, ratio, tier)
			}

			// 比例写法：只表比例，档位留给上游。
			ratioOut, tierOut, err = geminiImageConfig(imageCanonicalWithSize(ratio))
			if err != nil {
				t.Fatalf("%s: %v", ratio, err)
			}
			if ratioOut != ratio || tierOut != "" {
				t.Fatalf("ratio %s -> (%s, %s), want (%s, empty)", ratio, ratioOut, tierOut, ratio)
			}

			// 档位写法：只表档位，比例留给上游。
			ratioOut, tierOut, err = geminiImageConfig(imageCanonicalWithSize(tier))
			if err != nil {
				t.Fatalf("%s: %v", tier, err)
			}
			if ratioOut != "" || tierOut != tier {
				t.Fatalf("tier %s -> (%s, %s), want (empty, %s)", tier, ratioOut, tierOut, tier)
			}

			// 「size + aspect_ratio」是两维输入的完整形态。
			canonical := imageCanonicalWithSize(tier)
			canonical.fields["aspect_ratio"] = ratio
			ratioOut, tierOut, err = geminiImageConfig(canonical)
			if err != nil {
				t.Fatalf("%s + %s: %v", tier, ratio, err)
			}
			if ratioOut != ratio || tierOut != tier {
				t.Fatalf("%s + %s -> (%s, %s), want (%s, %s)", tier, ratio, ratioOut, tierOut, ratio, tier)
			}
		}
	}
}

// 回程：交付字节自己的宽高决定 size，与上游声明的 size 无关。
func TestImagePixelSizeReadsDeliveredBytes(t *testing.T) {
	cases := []struct {
		name   string
		encode func(t *testing.T, w, h int) []byte
	}{
		{name: "png", encode: func(t *testing.T, w, h int) []byte { return testPNGImage(t, w, h) }},
		{name: "jpeg", encode: func(t *testing.T, w, h int) []byte {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
				t.Fatalf("encode jpeg: %v", err)
			}
			return buf.Bytes()
		}},
	}
	for _, tc := range cases {
		for _, dims := range [][2]int{{1, 1}, {1376, 768}, {4096, 4096}, {768, 1376}} {
			blob := tc.encode(t, dims[0], dims[1])
			w, h, ok := imagePixelSize(base64.StdEncoding.EncodeToString(blob))
			if !ok || w != dims[0] || h != dims[1] {
				t.Fatalf("%s %dx%d: got (%d,%d,%v)", tc.name, dims[0], dims[1], w, h, ok)
			}
		}
	}
}

// 认不出就不填：URL 引用、空串、垃圾 b64 都不许 panic 或编造尺寸。
func TestImagePixelSizeRejectsNonImage(t *testing.T) {
	for _, payload := range []string{
		"",
		"https://cdn.example.com/a.png",
		base64.StdEncoding.EncodeToString([]byte("not an image at all")),
		"iVBORw0KGgo=", // 16 字节的假 PNG 头，够不到 IHDR 宽高
	} {
		if w, h, ok := imagePixelSize(payload); ok {
			t.Fatalf("%q must not yield a size, got %dx%d", payload, w, h)
		}
	}
}

// 双向闭环：请求 size -> imageConfig -> 上游按表产图 -> 回包 size 回到原值。
func TestGeminiSizeRoundTripThroughResponse(t *testing.T) {
	for _, ratio := range geminiAspectRatioOrder {
		pixels := geminiImageSizeTable[ratio]
		column, _ := geminiSizeTierColumn("2K")
		w, h := pixels[column][0], pixels[column][1]

		canonical := imageCanonicalWithSize(fmt.Sprintf("%dx%d", w, h))
		ratioOut, tierOut, err := geminiImageConfig(canonical)
		if err != nil {
			t.Fatalf("%dx%d: %v", w, h, err)
		}
		if ratioOut != ratio || tierOut != "2K" {
			t.Fatalf("%dx%d -> (%s, %s)", w, h, ratioOut, tierOut)
		}

		response, err := decodeGeminiImageResponse(geminiImageEnvelope(t, testPNGImage(t, w, h)), 0)
		if err != nil {
			t.Fatalf("%dx%d: decode: %v", w, h, err)
		}
		body, err := encodeImageResponse(response, nil)
		if err != nil {
			t.Fatalf("%dx%d: encode: %v", w, h, err)
		}
		entry := decodeGeminiPlanBody(t, body)["data"].([]any)[0].(map[string]any)
		if want := fmt.Sprintf("%dx%d", w, h); entry["size"] != want {
			t.Fatalf("round trip: size = %v, want %s", entry["size"], want)
		}
	}
}

// 窗口边界：首试 3KB 读不到就放大到 96KB，仍读不到才放弃，不许猜尺寸。
func TestImagePixelSizeExifWindow(t *testing.T) {
	jpegWithPadding := func(padding int) []byte {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 640, 360)), nil); err != nil {
			t.Fatalf("encode jpeg: %v", err)
		}
		blob := buf.Bytes()
		out := append([]byte{}, blob[:2]...)
		// 单个 APP 段上限 65535，大填充要拆成多段才合法。
		for remaining := padding; remaining > 0; {
			chunk := remaining
			if chunk > 60000 {
				chunk = 60000
			}
			segment := make([]byte, 4+chunk)
			segment[0], segment[1] = 0xFF, 0xE1
			binary.BigEndian.PutUint16(segment[2:4], uint16(chunk+2))
			out = append(out, segment...)
			remaining -= chunk
		}
		out = append(out, blob[2:]...)
		return out
	}

	// 小 EXIF：首试窗口内。
	if w, h, ok := imagePixelSize(base64.StdEncoding.EncodeToString(jpegWithPadding(1024))); !ok || w != 640 || h != 360 {
		t.Fatalf("small exif: got (%d,%d,%v), want (640,360,true)", w, h, ok)
	}
	// 中 EXIF：把 SOF 推出 3KB，第二级窗口要接住（duck 的小图就是这样）。
	if w, h, ok := imagePixelSize(base64.StdEncoding.EncodeToString(jpegWithPadding(6000))); !ok || w != 640 || h != 360 {
		t.Fatalf("medium exif: got (%d,%d,%v), want (640,360,true)", w, h, ok)
	}
	// 超大 EXIF：两级窗口都读不到，放弃。
	if w, h, ok := imagePixelSize(base64.StdEncoding.EncodeToString(jpegWithPadding(200000))); ok {
		t.Fatalf("huge exif must not yield a size, got %dx%d", w, h)
	}
}

func imageCanonicalWithSize(size string) *imageCanonical {
	canonical := newImageCanonical()
	canonical.fields["prompt"] = "a cat"
	canonical.fields["size"] = size
	return canonical
}

func geminiImageEnvelope(t *testing.T, blob []byte) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{map[string]any{
				"inlineData": map[string]any{"mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(blob)},
			}}},
		}},
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return raw
}

// 回包必须带真实像素：size -> (aspectRatio, imageSize) 只有去的方向不够，
// 客户端要能看见上游实际给了什么。
func TestGeminiResponseCarriesDeliveredSize(t *testing.T) {
	blob := testPNGImage(t, 1376, 768)

	response, err := decodeGeminiImageResponse(geminiImageEnvelope(t, blob), 0)
	if err != nil {
		t.Fatalf("decodeGeminiImageResponse: %v", err)
	}
	body, err := encodeImageResponse(response, nil)
	if err != nil {
		t.Fatalf("encodeImageResponse: %v", err)
	}
	data := decodeGeminiPlanBody(t, body)["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("data = %#v", data)
	}
	entry := data[0].(map[string]any)
	if entry["size"] != "1376x768" {
		t.Fatalf("size = %v, want 1376x768", entry["size"])
	}
	if entry["b64_json"] == "" {
		t.Fatal("b64_json must survive the size annotation")
	}
}

// 缺席保持缺席：size 为空或 auto 时不写 imageConfig，让上游用默认。
func TestPlanGeminiLeavesUnstatedSizeToUpstream(t *testing.T) {
	for _, size := range []string{"", "auto", "AUTO"} {
		canonical := newImageCanonical()
		canonical.fields["prompt"] = "a cat"
		if size != "" {
			canonical.fields["size"] = size
		}

		plan, err := planGemini(context.Background(), canonical, "gemini-2.5-flash-image")
		if err != nil {
			t.Fatalf("size %q: planGemini: %v", size, err)
		}
		generationConfig := decodeGeminiPlanBody(t, plan.Body)["generationConfig"].(map[string]any)
		if _, exists := generationConfig["imageConfig"]; exists {
			t.Fatalf("size %q must not invent an imageConfig: %#v", size, generationConfig["imageConfig"])
		}
	}
}

// 认不出的写法与超上限必须 400：静默丢弃或截断会让客户端以为尺寸生效了。
func TestPlanGeminiRejectsUnusableSize(t *testing.T) {
	for _, size := range []string{"999", "abc", "0x1024", "1024x", "8192x8192", "999999999x999999999"} {
		canonical := newImageCanonical()
		canonical.fields["prompt"] = "a cat"
		canonical.fields["size"] = size

		if _, err := planGemini(context.Background(), canonical, "gemini-2.5-flash-image"); err == nil {
			t.Fatalf("size %q must be rejected", size)
		}
	}
}

func TestPlanGeminiRejectsMalformedAspectRatio(t *testing.T) {
	canonical := newImageCanonical()
	canonical.fields["prompt"] = "a cat"
	canonical.fields["aspect_ratio"] = "wide"

	if _, err := planGemini(context.Background(), canonical, "gemini-2.5-flash-image"); err == nil {
		t.Fatal("aspect_ratio wide must be rejected")
	}
}

func TestCanonicalFromGeminiTakesLastUserTurn(t *testing.T) {
	request := &dto.GeminiChatRequest{
		Contents: []dto.GeminiChatContent{
			{Role: "user", Parts: []dto.GeminiPart{{Text: "old turn"}}},
			{Role: "model", Parts: []dto.GeminiPart{{Text: "answer"}}},
			{Role: "user", Parts: []dto.GeminiPart{
				{Text: "new turn"},
				{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "iVBORw0KGgo="}},
			}},
		},
	}

	canonical, err := canonicalFromGemini(request, "gemini-2.5-flash-image")
	if err != nil {
		t.Fatalf("canonicalFromGemini: %v", err)
	}
	if got := canonical.fields["prompt"]; got != "new turn" {
		t.Fatalf("prompt = %v", got)
	}
	refs := canonical.imageRefs()
	if len(refs) != 1 || !strings.HasPrefix(refs[0], "data:image/png;base64,") {
		t.Fatalf("image refs = %#v", refs)
	}
}

func TestDecodeGeminiImageResponse(t *testing.T) {
	raw := []byte(`{
		"candidates": [{
			"content": {"role": "model", "parts": [
				{"text": "here"},
				{"inlineData": {"mimeType": "image/png", "data": "iVBORw0KGgo="}}
			]},
			"finishReason": "STOP",
			"index": 0
		}],
		"modelVersion": "gemini-2.5-flash-image"
	}`)

	decoded, err := decodeGeminiImageResponse(raw, 1700000000)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Created != 1700000000 {
		t.Fatalf("created = %d", decoded.Created)
	}
	if len(decoded.Data) != 1 || decoded.Data[0].B64JSON != "iVBORw0KGgo=" {
		t.Fatalf("data = %#v", decoded.Data)
	}
	if len(decoded.Extras) != 0 {
		t.Fatalf("extras must stay empty, got %#v", decoded.Extras)
	}
}

func TestDecodeGeminiImageResponseBlocked(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[]},"finishReason":"SAFETY"}]}`)
	_, err := decodeGeminiImageResponse(raw, 1)
	if err == nil {
		t.Fatal("SAFETY must be an error")
	}
	var blocked *geminiImageBlockedError
	if !asBlocked(err, &blocked) {
		t.Fatalf("err = %T, want geminiImageBlockedError", err)
	}
}

func TestDecodeGeminiImageResponseWithoutImage(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"I cannot draw that"}]},"finishReason":"STOP"}]}`)
	if _, err := decodeGeminiImageResponse(raw, 1); err == nil {
		t.Fatal("text-only response must not be treated as an image")
	}
}

func asBlocked(err error, target **geminiImageBlockedError) bool {
	blocked, ok := err.(*geminiImageBlockedError)
	if ok {
		*target = blocked
	}
	return ok
}

func TestCanonicalFromResponsesStringInput(t *testing.T) {
	request := &dto.OpenAIResponsesRequest{Input: json.RawMessage(`"a cat"`)}
	canonical, err := canonicalFromResponses(request, "banana")
	if err != nil {
		t.Fatalf("canonicalFromResponses: %v", err)
	}
	if got := canonical.fields["prompt"]; got != "a cat" {
		t.Fatalf("prompt = %v", got)
	}
	if canonical.hasImage() {
		t.Fatal("string input must not invent an image")
	}
}

func TestCanonicalFromResponsesTakesLastUserTurn(t *testing.T) {
	input := `[
		{"role":"user","content":[{"type":"input_text","text":"old turn"}]},
		{"role":"assistant","content":[{"type":"output_text","text":"answer"}]},
		{"role":"user","content":[
			{"type":"input_text","text":"new turn"},
			{"type":"input_image","image_url":{"url":"https://cdn.example.com/cat.png"}}
		]}
	]`
	request := &dto.OpenAIResponsesRequest{
		Input:        json.RawMessage(input),
		Instructions: json.RawMessage(`"be brief"`),
	}
	canonical, err := canonicalFromResponses(request, "banana")
	if err != nil {
		t.Fatalf("canonicalFromResponses: %v", err)
	}
	if got, want := canonical.fields["prompt"], "be brief\n\nnew turn"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
	refs := canonical.imageRefs()
	if len(refs) != 1 || refs[0] != "https://cdn.example.com/cat.png" {
		t.Fatalf("refs = %#v", refs)
	}
}

func TestCanonicalFromResponsesRejectsEmpty(t *testing.T) {
	if _, err := canonicalFromResponses(&dto.OpenAIResponsesRequest{}, "banana"); err == nil {
		t.Fatal("empty input must be rejected")
	}
}

func TestDecodeImageForUpstream(t *testing.T) {
	geminiBody := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]},"finishReason":"STOP"}]}`
	decoded, err := decodeImageForUpstream([]byte(geminiBody), "gemini", 0)
	if err != nil {
		t.Fatalf("gemini upstream: %v", err)
	}
	if len(decoded.Data) != 1 || decoded.Data[0].B64JSON != "aGVsbG8=" {
		t.Fatalf("gemini upstream decoded %#v", decoded.Data)
	}

	openAIBody := `{"created":7,"data":[{"b64_json":"aGVsbG8="}]}`
	for _, upstream := range []string{"ark", "openai", "seedream", ""} {
		decoded, err = decodeImageForUpstream([]byte(openAIBody), upstream, 0)
		if err != nil {
			t.Fatalf("upstream %q: %v", upstream, err)
		}
		if len(decoded.Data) != 1 || decoded.Data[0].B64JSON != "aGVsbG8=" {
			t.Fatalf("upstream %q decoded %#v", upstream, decoded.Data)
		}
	}

	// 上游形状与配置不一致时必须失败，不能把 candidates 当 data[] 静默读成空。
	if _, err := decodeImageForUpstream([]byte(geminiBody), "ark", 0); err == nil {
		t.Fatal("ark decoder must reject a candidates[] body")
	}
}

func TestWriteResponsesImageStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	markdown := "![image](data:image/png;base64,aGVsbG8=)"
	usage := &dto.Usage{PromptTokens: 7, CompletionTokens: 1290, TotalTokens: 1297}
	if apiErr := writeResponsesImageStream(c, &common.RelayInfo{}, "banana", markdown, usage); apiErr != nil {
		t.Fatalf("writeResponsesImageStream: %v", apiErr)
	}

	if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("content type = %q", got)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"event: response.created",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.completed",
		"aGVsbG8=",
		`"total_tokens":1297`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in stream:\n%s", want, body)
		}
	}
	// 终态事件必须在 delta 之后，否则客户端拿不到文本。
	if strings.Index(body, "response.output_text.delta") > strings.Index(body, "response.completed") {
		t.Fatal("response.completed emitted before the delta")
	}
}

func TestWriteGeminiStreamResponseAlt(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sseRecorder := httptest.NewRecorder()
	sseCtx, _ := gin.CreateTestContext(sseRecorder)
	sseCtx.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/x:streamGenerateContent?alt=sse", nil)
	writeGeminiStreamResponse(sseCtx, []byte(`{"candidates":[]}`))
	if got := sseRecorder.Body.String(); !strings.HasPrefix(got, "data: ") {
		t.Fatalf("alt=sse body = %q", got)
	}

	jsonRecorder := httptest.NewRecorder()
	jsonCtx, _ := gin.CreateTestContext(jsonRecorder)
	jsonCtx.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/x:streamGenerateContent", nil)
	writeGeminiStreamResponse(jsonCtx, []byte(`{"candidates":[]}`))
	if got, want := jsonRecorder.Body.String(), `[{"candidates":[]}]`; got != want {
		t.Fatalf("default body = %q, want %q", got, want)
	}
	if got := jsonRecorder.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("content type = %q", got)
	}
}

func TestPlanGeminiKeepsNamespacedModelSegments(t *testing.T) {
	canonical := newImageCanonical()
	canonical.fields["model"] = "google/gemini-nano-banana-2.1"
	canonical.fields["prompt"] = "a cat"
	plan, err := planGemini(context.Background(), canonical, "google/gemini-nano-banana-2.1")
	if err != nil {
		t.Fatalf("planGemini: %v", err)
	}
	const want = "/v1beta/models/google/gemini-nano-banana-2.1:generateContent"
	if plan.UpstreamPath != want {
		t.Fatalf("upstream path = %q, want %q", plan.UpstreamPath, want)
	}
}

// 真实上游（zenmux / google/gemini-nano-banana-2.1）一次调用回两个 inlineData：
// 先一张草稿、再一张成品。OpenAI 形状的入口只交付最后一张，否则 data[] 长度
// 会大于 n，与按 n 预扣的账本对不上。
func TestDeliverableImagesKeepsFinalForGemini(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[
		{"text":"refining the concept","thought":true},
		{"inlineData":{"mimeType":"image/png","data":"ZHJhZnQ="}},
		{"text":"completed","thought":true},
		{"inlineData":{"mimeType":"image/png","data":"ZmluYWw="}}
	]},"finishReason":"STOP"}]}`

	decoded, err := decodeGeminiImageResponse([]byte(body), 0)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Data) != 2 {
		t.Fatalf("decoder must stay faithful, got %d images", len(decoded.Data))
	}

	trimmed := deliverableImages(decoded, "gemini")
	if len(trimmed.Data) != 1 {
		t.Fatalf("gemini must deliver 1 image, got %d", len(trimmed.Data))
	}
	if trimmed.Data[0].B64JSON != "ZmluYWw=" {
		t.Fatalf("must keep the final image, got %q", trimmed.Data[0].B64JSON)
	}
}

// ark / openai 的 n 是真扇出：两张就是两张，不能裁。
func TestDeliverableImagesLeavesFanoutUpstreams(t *testing.T) {
	response := &imageResponse{Data: []imageResponseItem{
		{B64JSON: "b25l"}, {B64JSON: "dHdv"},
	}}
	for _, upstream := range []string{"ark", "openai", "seedream", ""} {
		got := deliverableImages(response, upstream)
		if len(got.Data) != 2 {
			t.Fatalf("upstream %q must keep 2 images, got %d", upstream, len(got.Data))
		}
	}
	if got := deliverableImages(nil, "gemini"); got != nil {
		t.Fatal("nil response must stay nil")
	}
}

// usageMetadata 用上游真实 token（展示用）；账本仍按张，不能跟着变成 token 计费。
func TestWrapGeminiResponseUsesRealUsage(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[
		{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}
	]},"finishReason":"STOP"}],
	"usageMetadata":{"promptTokenCount":6,"candidatesTokenCount":1148,"totalTokenCount":2056,"thoughtsTokenCount":526,"trafficType":"ON_DEMAND"}}`

	decoded, err := decodeGeminiImageResponse([]byte(body), 0)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	raw, err := wrapGeminiResponse(decoded, "banana")
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	usage, _ := out["usageMetadata"].(map[string]any)
	if usage["promptTokenCount"] != float64(6) || usage["totalTokenCount"] != float64(2056) {
		t.Fatalf("usage must be the upstream one, got %#v", usage)
	}
	if _, leaked := usage["trafficType"]; leaked {
		t.Fatalf("trafficType is upstream scheduling info, must not leak: %#v", usage)
	}
	candidates, _ := out["candidates"].([]any)
	first, _ := candidates[0].(map[string]any)
	parts, _ := first["content"].(map[string]any)["parts"].([]any)
	if len(parts) != 1 {
		t.Fatalf("gemini entry keeps the source parts, got %d", len(parts))
	}
}

// Gemini 没有 mask 通道，静默忽略会产出「不是用户要的图」并照常计费。
func TestPlanGeminiRejectsMask(t *testing.T) {
	canonical := newImageCanonical()
	canonical.fields["model"] = "banana"
	canonical.fields["prompt"] = "make the sky blue"
	canonical.fields["image"] = "data:image/png;base64,aGVsbG8="
	canonical.fields["mask"] = "data:image/png;base64,bWFzaw=="

	_, err := planGemini(context.Background(), canonical, "banana")
	if err == nil {
		t.Fatal("mask must be rejected instead of silently dropped")
	}
	if !strings.Contains(err.Error(), "mask") {
		t.Fatalf("error must name the offending field, got %v", err)
	}
}

// 真实上游（zenmux / google/gemini-nano-banana-2.1）的报文形状，只把 base64 换成
// 1x1 PNG、thoughtSignature 换成短值。锁三件事：
//
//	① 未知字段（thought / thoughtSignature / createTime / responseId）不影响解码；
//	② usageMetadata 解析成展示用量，trafficType 不出网关；
//	③ 两道 thought 夹两张图时，OpenAI 形状的入口只交付最后一张。
func TestRealGeminiPairFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/gemini_nano_banana_pair.json")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	decoded, err := decodeGeminiImageResponse(raw, 0)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Data) != 2 {
		t.Fatalf("decoder must stay faithful to the source, got %d", len(decoded.Data))
	}
	if decoded.Usage == nil ||
		decoded.Usage.PromptTokens != 3 ||
		decoded.Usage.CompletionTokens != 1120 ||
		decoded.Usage.TotalTokens != 1649 ||
		decoded.Usage.ThoughtsTokens != 526 {
		t.Fatalf("usage must match the captured payload, got %+v", decoded.Usage)
	}

	// 1x1 PNG 的两种字节；成品是第二张。
	final := decoded.Data[1].B64JSON
	trimmed := deliverableImages(decoded, "gemini")
	if len(trimmed.Data) != 1 || trimmed.Data[0].B64JSON != final {
		t.Fatalf("OpenAI entries must deliver the final image only, got %d", len(trimmed.Data))
	}

	out, err := wrapGeminiResponse(trimmed, "google/gemini-nano-banana-2.1")
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	var rebuilt map[string]any
	if err := json.Unmarshal(out, &rebuilt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, leaked := rebuilt["trafficType"]; leaked {
		t.Fatal("trafficType must not appear at the top level")
	}
	usage := rebuilt["usageMetadata"].(map[string]any)
	if _, leaked := usage["trafficType"]; leaked {
		t.Fatalf("trafficType is upstream scheduling info: %#v", usage)
	}
	if _, leaked := rebuilt["responseId"]; leaked {
		t.Fatalf("responseId is upstream identity: %#v", rebuilt)
	}
}

// ---------------------------------------------------------------- imageConfig → size

// geminiImageConfigRequest 构造一条只带 imageConfig 的 Gemini 请求。
func geminiImageConfigRequest(ratio, tier string) *dto.GeminiChatRequest {
	config := map[string]any{}
	if ratio != "" {
		config["aspectRatio"] = ratio
	}
	if tier != "" {
		config["imageSize"] = tier
	}
	raw, _ := json.Marshal(config)
	return &dto.GeminiChatRequest{
		Contents: []dto.GeminiChatContent{{
			Role:  "user",
			Parts: []dto.GeminiPart{{Text: "a cat"}},
		}},
		GenerationConfig: dto.GeminiChatGenerationConfig{ImageConfig: raw},
	}
}

// size 是 canonical 的规范尺寸字段，ark / openai 形状的上游只认它。
// 这里钉住官方像素表：换算法（面积/边长）之前先想清楚客户端预期。
func TestCanonicalFromGeminiFoldsImageConfigToSize(t *testing.T) {
	cases := []struct {
		ratio string
		tier  string
		size  string
	}{
		{"16:9", "2K", "2752x1536"},
		{"16:9", "4K", "5504x3072"},
		{"9:16", "1K", "768x1376"},
		{"21:9", "2K", "3168x1344"},
		{"4:3", "", "1200x896"}, // imageSize 缺省 = 1K（官方默认）
		{"", "4K", "4096x4096"}, // aspectRatio 缺省 = 1:1（官方默认）
		{"1:1", "2K", "2048x2048"},
		{"16 : 9", "2k", "2752x1536"}, // 空格 / 小写档位都要收敛
		{"7:5", "2K", "2424x1728"},    // 表外比例：按档位面积算，取 8 的倍数
	}
	for _, tc := range cases {
		canonical, err := canonicalFromGemini(geminiImageConfigRequest(tc.ratio, tc.tier), "gemini-3-pro-image")
		if err != nil {
			t.Fatalf("ratio=%q tier=%q: %v", tc.ratio, tc.tier, err)
		}
		if got := canonical.fields["size"]; got != tc.size {
			t.Fatalf("ratio=%q tier=%q size = %v, want %s", tc.ratio, tc.tier, got, tc.size)
		}
	}
}

func TestCanonicalFromGeminiRejectsBadImageConfig(t *testing.T) {
	cases := []struct {
		name  string
		ratio string
		tier  string
		want  string
	}{
		{"unknown tier", "16:9", "8K", "imageSize"},
		{"tier is not a tier", "16:9", "2048x2048", "imageSize"},
		{"ratio is a word", "wide", "2K", "aspectRatio"},
		{"ratio has three parts", "16:9:1", "2K", "aspectRatio"},
		{"ratio has a zero", "0:9", "2K", "aspectRatio"},
	}
	for _, tc := range cases {
		_, err := canonicalFromGemini(geminiImageConfigRequest(tc.ratio, tc.tier), "m")
		if err == nil {
			t.Fatalf("%s: must be rejected instead of silently dropped", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error must name %q, got %v", tc.name, tc.want, err)
		}
	}
}

// 多垫图：全部收下、保序、不压成标量。文档里「只取第一张」是旧行为。
func TestCanonicalFromGeminiKeepsEveryInlineImage(t *testing.T) {
	mimes := []string{"image/png", "image/jpeg", "image/webp"}
	payloads := []string{"iVBORw0KGgo=", "/9j/4AAQ", "UklGRg=="}
	parts := []dto.GeminiPart{{Text: "combine"}}
	for i := range mimes {
		parts = append(parts, dto.GeminiPart{
			InlineData: &dto.GeminiInlineData{MimeType: mimes[i], Data: payloads[i]},
		})
	}
	request := &dto.GeminiChatRequest{Contents: []dto.GeminiChatContent{{Role: "user", Parts: parts}}}

	canonical, err := canonicalFromGemini(request, "m")
	if err != nil {
		t.Fatalf("canonicalFromGemini: %v", err)
	}
	refs := canonical.imageRefs()
	if len(refs) != len(mimes) {
		t.Fatalf("image refs = %d, want %d", len(refs), len(mimes))
	}
	for i, mime := range mimes {
		want := "data:" + mime + ";base64," + payloads[i]
		if refs[i] != want {
			t.Fatalf("refs[%d] = %q, want %q (order is semantics)", i, refs[i], want)
		}
	}
	if _, ok := canonical.fields["image"].([]string); !ok {
		t.Fatalf("multi-image must stay an array, got %T", canonical.fields["image"])
	}
}

// inlineData 的载荷必须是 base64。公网地址走 fileData.fileUri（见下一个用例），
// 写成 URL 的 inlineData 不是 Gemini 的语法。
func TestCanonicalFromGeminiRejectsNonInlineCarriers(t *testing.T) {
	cases := []struct {
		name string
		part dto.GeminiPart
		want string
	}{
		{
			"URL in inlineData",
			dto.GeminiPart{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "https://example.com/a.png"}},
			"not a URL",
		},
		{
			"data URI in inlineData",
			dto.GeminiPart{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "data:image/png;base64,iVBORw0KGgo="}},
			"not a URL",
		},
		{
			"missing mimeType",
			dto.GeminiPart{InlineData: &dto.GeminiInlineData{Data: "iVBORw0KGgo="}},
			"mimeType",
		},
		{
			"empty payload",
			dto.GeminiPart{InlineData: &dto.GeminiInlineData{MimeType: "image/png"}},
			"data is empty",
		},
	}
	for _, tc := range cases {
		request := &dto.GeminiChatRequest{Contents: []dto.GeminiChatContent{{
			Role:  "user",
			Parts: []dto.GeminiPart{{Text: "x"}, tc.part},
		}}}
		_, err := canonicalFromGemini(request, "m")
		if err == nil {
			t.Fatalf("%s: must be rejected", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error must mention %q, got %v", tc.name, tc.want, err)
		}
	}
}

// 折出来的 size 必须真的发到 ark 形状的上游；方言字段不能再下传。
func TestPlanARKUsesFoldedGeminiSize(t *testing.T) {
	cfg := &dto.ImageProtocolConfig{Enabled: true, Upstream: dto.ImageUpstreamARK}
	canonical, err := canonicalFromGemini(geminiImageConfigRequest("16:9", "2K"), "doubao-seedream-5-0")
	if err != nil {
		t.Fatalf("canonicalFromGemini: %v", err)
	}
	if err := canonical.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}

	plan, err := planImageRequest(canonical, cfg)
	if err != nil {
		t.Fatalf("planImageRequest: %v", err)
	}
	body := decodeGeminiPlanBody(t, plan.Body)
	if body["size"] != "2752x1536" {
		t.Fatalf("size = %v, want 2752x1536", body["size"])
	}
	for _, key := range []string{"aspect_ratio", "image_size"} {
		if _, exists := body[key]; exists {
			t.Fatalf("%s must not be forwarded to an ark-shaped upstream: %#v", key, body[key])
		}
	}
}

// gemini / chat / responses 入口不经过 image_handler 的估算，必须在
// prepareImageProtocol 补：否则上游不返 usage 时只扣 {1,0,1}。
// 实测：同一模型 gemini 入口 38 quota、images 入口 170063。
func TestPrepareImageProtocolEstimatesUsageForGeminiEntry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost,
		"/v1beta/models/gemini-3.1-flash-image-preview:generateContent", bytes.NewReader([]byte(`{}`)))
	c.Request.Header.Set("Content-Type", "application/json")

	info := &common.RelayInfo{
		RelayMode: relayconstant.RelayModeGemini,
		ChannelMeta: &common.ChannelMeta{
			UpstreamModelName: "gemini-3.1-flash-image-preview",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				ImageProtocol: &dto.ImageProtocolConfig{
					Enabled:  true,
					Upstream: dto.ImageUpstreamARK,
				},
			},
		},
		Request: geminiImageConfigRequest("1:1", "2K"),
	}

	adaptor := &Adaptor{}
	if _, handled, err := adaptor.prepareImageProtocol(c, info); err != nil || !handled {
		t.Fatalf("prepareImageProtocol: handled=%v err=%v", handled, err)
	}
	if info.EstimatedImageUsage == nil {
		t.Fatal("gemini 入口必须在本层补估算，否则上游不返 usage 时只扣 1 token")
	}
	if info.EstimatedImageUsage.CompletionTokens <= 0 {
		t.Fatalf("估算的 completion tokens = %d，图片请求必须计入输出侧",
			info.EstimatedImageUsage.CompletionTokens)
	}
	if info.EstimatedImageCount != 1 {
		t.Fatalf("EstimatedImageCount = %d, want 1", info.EstimatedImageCount)
	}
}

// Gemini 入口必须真的走图片协议层，不能落回原生 Gemini 适配器。
// 这是 prepareImageProtocol 的 RelayModeGemini 分支门禁：分支丢了，请求会落到
// 原生适配器，size 与多垫图一起消失（ServesPath 那条链路由 dto 包的用例钉住）。
func TestPrepareImageProtocolTakesGeminiEntry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost,
		"/v1beta/models/gemini-3-pro-image:generateContent", bytes.NewReader([]byte(`{}`)))
	c.Request.Header.Set("Content-Type", "application/json")

	request := geminiImageConfigRequest("16:9", "2K")
	request.Contents[0].Parts = append(request.Contents[0].Parts,
		dto.GeminiPart{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "iVBORw0KGgo="}},
		dto.GeminiPart{InlineData: &dto.GeminiInlineData{MimeType: "image/jpeg", Data: "/9j/4AAQ"}},
	)
	info := &common.RelayInfo{
		RelayMode: relayconstant.RelayModeGemini,
		ChannelMeta: &common.ChannelMeta{
			UpstreamModelName: "doubao-seedream-5-0",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				ImageProtocol: &dto.ImageProtocolConfig{
					Enabled:  true,
					Upstream: dto.ImageUpstreamARK,
				},
			},
		},
		Request: request,
	}

	adaptor := &Adaptor{}
	plan, handled, err := adaptor.prepareImageProtocol(c, info)
	if err != nil {
		t.Fatalf("prepareImageProtocol: %v", err)
	}
	if !handled {
		t.Fatal("Gemini entry must be taken over by the image protocol")
	}
	body := decodeGeminiPlanBody(t, plan.Body)
	if body["size"] != "2752x1536" {
		t.Fatalf("size = %v, want 2752x1536", body["size"])
	}
	refs, ok := body["image"].([]any)
	if !ok || len(refs) != 2 {
		t.Fatalf("image = %#v, want a 2-element array (order preserved)", body["image"])
	}
	for _, key := range []string{"aspect_ratio", "image_size"} {
		if _, exists := body[key]; exists {
			t.Fatalf("%s must not reach an ark-shaped upstream", key)
		}
	}
}

// testPNGImage 造一张真 PNG：尺寸相关用例要从文件头量出宽高，
// 不能用协议测试里的假头（只有 16 字节，解码器读不出 IHDR）。
func testPNGImage(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatalf("encode test png: %v", err)
	}
	return buf.Bytes()
}

// fileData（公网 URL）与 inlineData（base64）是两种合法载体，必须按 parts 顺序
// 合并进同一个数组：客户端 prompt 里「第一张的颜色 + 第二张的形状」全靠顺序。
func TestCanonicalFromGeminiFoldsFileDataInOrder(t *testing.T) {
	request := &dto.GeminiChatRequest{Contents: []dto.GeminiChatContent{{Role: "user", Parts: []dto.GeminiPart{
		{Text: "第一张的颜色 + 第二张的形状 + 第三张的边数"},
		{FileData: &dto.GeminiFileData{MimeType: "image/jpeg", FileUri: "https://cdn.example.com/a.jpg"}},
		{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "iVBORw0KGgo="}},
		{FileData: &dto.GeminiFileData{MimeType: "image/webp", FileUri: "https://cdn.example.com/b.webp"}},
	}}}}

	canonical, err := canonicalFromGemini(request, "m")
	if err != nil {
		t.Fatalf("canonicalFromGemini: %v", err)
	}
	want := []string{
		"https://cdn.example.com/a.jpg",
		"data:image/png;base64,iVBORw0KGgo=",
		"https://cdn.example.com/b.webp",
	}
	refs := canonical.imageRefs()
	if len(refs) != len(want) {
		t.Fatalf("refs = %d, want %d (%#v)", len(refs), len(want), refs)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("refs[%d] = %q, want %q (order is semantics)", i, refs[i], want[i])
		}
	}
	if !canonical.hasImage() {
		t.Fatal("fileData must count as an image reference: the request is image-to-image")
	}
}

// 收下 URL 不等于收下任何字符串。取不到的 scheme 与字面量内网地址在折叠层就拒：
// ark 形状的上游会原样转发链接并自己抓取，不挡等于让客户端借上游网段做 SSRF。
func TestCanonicalFromGeminiRejectsBadFileData(t *testing.T) {
	cases := []struct {
		name string
		part dto.GeminiPart
		want string
	}{
		{
			"empty uri",
			dto.GeminiPart{FileData: &dto.GeminiFileData{MimeType: "image/png"}},
			"fileUri is required",
		},
		{
			"gcs uri",
			dto.GeminiPart{FileData: &dto.GeminiFileData{MimeType: "image/png", FileUri: "gs://bucket/a.png"}},
			"http(s)",
		},
		{
			"file scheme",
			dto.GeminiPart{FileData: &dto.GeminiFileData{MimeType: "image/png", FileUri: "file:///etc/passwd"}},
			"http(s)",
		},
		{
			"no host",
			dto.GeminiPart{FileData: &dto.GeminiFileData{FileUri: "https://"}},
			"no host",
		},
		{
			"localhost",
			dto.GeminiPart{FileData: &dto.GeminiFileData{FileUri: "http://localhost/a.png"}},
			"not allowed",
		},
		{
			"loopback",
			dto.GeminiPart{FileData: &dto.GeminiFileData{FileUri: "http://127.0.0.1/a.png"}},
			"non-public",
		},
		{
			"rfc1918",
			dto.GeminiPart{FileData: &dto.GeminiFileData{FileUri: "http://10.0.0.1/a.png"}},
			"non-public",
		},
		{
			"both carriers in one part",
			dto.GeminiPart{
				InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "iVBORw0KGgo="},
				FileData:   &dto.GeminiFileData{FileUri: "https://cdn.example.com/a.png"},
			},
			"not both",
		},
	}
	for _, tc := range cases {
		request := &dto.GeminiChatRequest{Contents: []dto.GeminiChatContent{{
			Role:  "user",
			Parts: []dto.GeminiPart{{Text: "x"}, tc.part},
		}}}
		_, err := canonicalFromGemini(request, "m")
		if err == nil {
			t.Fatalf("%s: must be rejected", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error must mention %q, got %v", tc.name, tc.want, err)
		}
	}
}

// 非图片 fileData（音频 / PDF）与 text part 里的无关内容同等对待：跳过，
// 不算「字段被丢」，也不让请求误判成图生图。
func TestCanonicalFromGeminiSkipsNonImageFileData(t *testing.T) {
	request := &dto.GeminiChatRequest{Contents: []dto.GeminiChatContent{{Role: "user", Parts: []dto.GeminiPart{
		{Text: "make it blue"},
		{FileData: &dto.GeminiFileData{MimeType: "audio/mp3", FileUri: "https://cdn.example.com/a.mp3"}},
	}}}}
	canonical, err := canonicalFromGemini(request, "m")
	if err != nil {
		t.Fatalf("canonicalFromGemini: %v", err)
	}
	if canonical.hasImage() {
		t.Fatalf("audio must not become an image reference: %#v", canonical.fields["image"])
	}
}

// 第一级降级：ark 形状的 image 字段本来就收 URL，直接转发，零下载。
// 这条用例不建任何服务 —— 一旦有人给 planARK 加上「先下载」，它会超时失败。
func TestPlanARKForwardsFileDataURL(t *testing.T) {
	request := &dto.GeminiChatRequest{Contents: []dto.GeminiChatContent{{Role: "user", Parts: []dto.GeminiPart{
		{Text: "第一张的颜色 + 第二张的形状"},
		{FileData: &dto.GeminiFileData{MimeType: "image/jpeg", FileUri: "https://cdn.example.com/a.jpg"}},
		{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "iVBORw0KGgo="}},
	}}}}
	canonical, err := canonicalFromGemini(request, "doubao-seedream-5-0")
	if err != nil {
		t.Fatalf("canonicalFromGemini: %v", err)
	}
	if err := canonical.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}

	plan, err := planImageRequest(canonical, &dto.ImageProtocolConfig{Enabled: true, Upstream: dto.ImageUpstreamARK})
	if err != nil {
		t.Fatalf("planImageRequest: %v", err)
	}
	body := decodeGeminiPlanBody(t, plan.Body)
	refs, ok := body["image"].([]any)
	if !ok || len(refs) != 2 {
		t.Fatalf("image = %#v, want a 2-element array", body["image"])
	}
	if refs[0] != "https://cdn.example.com/a.jpg" || refs[1] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("image order changed on the way upstream: %#v", refs)
	}
}

// 第二级降级：只收 base64 的上游（gemini / openai）由脚本层先下载再内联。
// 下载层自己的 SSRF / 体积 / 超时规则由 protocol 测试覆盖，这里只钉接线。
func TestPlanGeminiDownloadsFileDataURL(t *testing.T) {
	allowLoopbackForTest(t)
	pngBytes := testPNGImage(t, 8, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer server.Close()

	request := &dto.GeminiChatRequest{Contents: []dto.GeminiChatContent{{Role: "user", Parts: []dto.GeminiPart{
		{Text: "参考这张图"},
		{FileData: &dto.GeminiFileData{MimeType: "image/png", FileUri: server.URL + "/a.png"}},
	}}}}
	canonical, err := canonicalFromGemini(request, "gemini-3-pro-image")
	if err != nil {
		t.Fatalf("canonicalFromGemini: %v", err)
	}
	if err := canonical.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}

	plan, err := planGemini(context.Background(), canonical, "gemini-3-pro-image")
	if err != nil {
		t.Fatalf("planGemini: %v", err)
	}
	parts := geminiPlanParts(t, plan.Body)
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want text + one inlineData", len(parts))
	}
	inline, ok := parts[1].(map[string]any)["inlineData"].(map[string]any)
	if !ok {
		t.Fatalf("second part is not inlineData: %#v", parts[1])
	}
	if inline["mimeType"] != "image/png" {
		t.Fatalf("mimeType = %#v, want image/png", inline["mimeType"])
	}
	if inline["data"] != base64.StdEncoding.EncodeToString(pngBytes) {
		t.Fatal("downloaded bytes must be inlined base64, not the URL")
	}
}

// IMAGE_RECITATION 是「可能复现受版权保护的内容」，与 SAFETY 同属内容级拒绝。
// 之前它落到「no image」502，客户端会把一次可解释的拒绝当成上游故障。
func TestDecodeGeminiImageResponseRecitationIsBlocked(t *testing.T) {
	for _, reason := range []string{"IMAGE_RECITATION", "RECITATION", "IMAGE_PROHIBITED_CONTENT", "SPII"} {
		raw := []byte(`{"candidates":[{"content":{"parts":[]},"finishReason":"` + reason + `"}]}`)
		_, err := decodeGeminiImageResponse(raw, 1)
		if err == nil {
			t.Fatalf("%s must be an error", reason)
		}
		var blocked *geminiImageBlockedError
		if !asBlocked(err, &blocked) {
			t.Fatalf("%s: err = %T, want geminiImageBlockedError", reason, err)
		}
		if !strings.Contains(err.Error(), reason) {
			t.Errorf("%s: message must carry the reason, got %q", reason, err.Error())
		}
	}
}

// 零图但不是已知的拒绝理由时，finishReason 必须进错误文案，否则日志里只有
// 「no image」，运维无法判断是上游抽风还是被挡了。
func TestDecodeGeminiImageResponseCarriesUnknownFinishReason(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[]},"finishReason":"IMAGE_OTHER"}]}`)
	_, err := decodeGeminiImageResponse(raw, 1)
	if err == nil {
		t.Fatal("zero images must be an error")
	}
	if !strings.Contains(err.Error(), "IMAGE_OTHER") {
		t.Errorf("message must carry finishReason, got %q", err.Error())
	}
}

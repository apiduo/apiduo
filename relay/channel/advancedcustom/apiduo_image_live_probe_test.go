//go:build apiduo_live

package advancedcustom

/*
 * 对真实上游跑的手工探针（不在 CI 里跑）。
 *
 * 它验证的是「折叠层 → 上游脚本」这一段在真实链路上的效果：Gemini 入口的
 * imageConfig、多垫图、fileData 公网引用，最后都要在出图上兑现。会真的出图
 * （单张 10~25s，上游额度自理），所以用独立 build tag 隔离：
 *
 *	IMAGE_PROBE_BASE_URL=https://<upstream> \
 *	IMAGE_PROBE_API_KEY=sk-xxx IMAGE_PROBE_MODEL=<model> \
 *	go test -tags apiduo_live -run TestLiveProbe -v ./relay/channel/advancedcustom/
 *
 * 探针不属于适配层：这里只出现上游形状（ark），不出现任何厂商分支。
 * 2026-11-25 用 duck.1task.cn 实测过三项（尺寸 / 多垫图 / URL 垫图）。
 *
 * 产物落在 /tmp/apiduo-image-probe/，用来人眼复核（断言只能证明「有图」，
 * 证明不了「第一张的颜色 + 第二张的形状」）。
 */

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

const liveProbeDir = "/tmp/apiduo-image-probe"

func liveProbeEnv(t *testing.T) (base, key, model string) {
	t.Helper()
	base = strings.TrimRight(strings.TrimSpace(os.Getenv("IMAGE_PROBE_BASE_URL")), "/")
	key = strings.TrimSpace(os.Getenv("IMAGE_PROBE_API_KEY"))
	model = strings.TrimSpace(os.Getenv("IMAGE_PROBE_MODEL"))
	if base == "" || key == "" {
		t.Skip("set IMAGE_PROBE_BASE_URL and IMAGE_PROBE_API_KEY to run the live probe")
	}
	if model == "" {
		model = "gpt-image-2.5"
	}
	return base, key, model
}

// liveProbePlan 是「Gemini 请求 → canonical → ark 脚本」的完整去程。
func liveProbePlan(t *testing.T, request *dto.GeminiChatRequest, model string) (*imageCanonical, *imagePlan) {
	t.Helper()
	canonical, err := canonicalFromGemini(request, model)
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
	return canonical, plan
}

// liveProbePost 把 plan 原样发给上游，并走真实的解码。
func liveProbePost(t *testing.T, base, key string, plan *imagePlan) (*imageResponse, time.Duration) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, base+plan.UpstreamPath, bytes.NewReader(plan.Body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", plan.ContentType)
	request.Header.Set("Authorization", "Bearer "+key)

	client := &http.Client{Timeout: 180 * time.Second}
	started := time.Now()
	resp, err := client.Do(request)
	if err != nil {
		t.Fatalf("upstream request: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, imageLargeResponseMaxBytes+1))
	if err != nil {
		t.Fatalf("read upstream response: %v", err)
	}
	elapsed := time.Since(started)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upstream returned %d in %s: %s", resp.StatusCode, elapsed, truncateForLog(string(raw)))
	}
	decoded, err := decodeImageResponse(raw)
	if err != nil {
		t.Fatalf("decodeImageResponse: %v", err)
	}
	return decoded, elapsed
}

// liveProbeSave 把出图落盘：URL 直接下载，b64 解码。返回实际像素。
func liveProbeSave(t *testing.T, name string, item imageResponseItem) (string, int, int) {
	t.Helper()
	if err := os.MkdirAll(liveProbeDir, 0o755); err != nil {
		t.Fatalf("mkdir probe dir: %v", err)
	}
	var raw []byte
	switch {
	case item.B64JSON != "":
		decoded, err := base64.StdEncoding.DecodeString(item.B64JSON)
		if err != nil {
			t.Fatalf("%s: decode b64: %v", name, err)
		}
		raw = decoded
	case item.URL != "":
		resp, err := (&http.Client{Timeout: 120 * time.Second}).Get(item.URL)
		if err != nil {
			t.Fatalf("%s: download result: %v", name, err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			t.Fatalf("%s: read result: %v", name, err)
		}
		raw = body
	default:
		t.Fatalf("%s: item has neither url nor b64_json", name)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s: decode result header: %v", name, err)
	}
	path := filepath.Join(liveProbeDir, name+".png")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("%s: write %s: %v", name, path, err)
	}
	t.Logf("%s: %s (%dx%d, %d bytes)", name, path, config.Width, config.Height, len(raw))
	return path, config.Width, config.Height
}

// 用例 1：Gemini 入口的 imageConfig 要在真实出图上兑现成精确像素。
func TestLiveProbeGeminiImageConfig(t *testing.T) {
	base, key, model := liveProbeEnv(t)
	request := &dto.GeminiChatRequest{Contents: []dto.GeminiChatContent{{Role: "user", Parts: []dto.GeminiPart{
		{Text: "a single flat teal triangle centered on a pure white background, minimal icon"},
	}}}}
	request.GenerationConfig.ImageConfig = []byte(`{"aspectRatio":"16:9","imageSize":"2K"}`)

	canonical, plan := liveProbePlan(t, request, model)
	if canonical.fields["size"] != "2752x1536" {
		t.Fatalf("folded size = %#v, want 2752x1536", canonical.fields["size"])
	}
	body := decodeGeminiPlanBody(t, plan.Body)
	if body["size"] != "2752x1536" {
		t.Fatalf("upstream size = %#v, want 2752x1536", body["size"])
	}
	for _, dropped := range []string{"aspect_ratio", "image_size"} {
		if _, exists := body[dropped]; exists {
			t.Fatalf("%s must not be forwarded to a non-gemini upstream", dropped)
		}
	}

	decoded, elapsed := liveProbePost(t, base, key, plan)
	if len(decoded.Data) != 1 {
		t.Fatalf("data = %d, want 1", len(decoded.Data))
	}
	t.Logf("upstream: %s; usage = %#v; revised = %q",
		elapsed, decoded.Extras["usage"], decoded.Data[0].Extras["revised_prompt"])
	_, width, height := liveProbeSave(t, "1-imageconfig-16x9-2k", decoded.Data[0])
	if width != 2752 || height != 1536 {
		t.Fatalf("delivered %dx%d, want 2752x1536", width, height)
	}
}

// 用例 2：多垫图必须两张都进上游（顺序即语义）。
func TestLiveProbeGeminiMultiReference(t *testing.T) {
	base, key, model := liveProbeEnv(t)
	red := liveProbeShape(t, color.RGBA{R: 220, G: 30, B: 30, A: 255}, "circle")
	blue := liveProbeShape(t, color.RGBA{R: 30, G: 60, B: 220, A: 255}, "square")

	request := &dto.GeminiChatRequest{Contents: []dto.GeminiChatContent{{Role: "user", Parts: []dto.GeminiPart{
		{Text: "Use the COLOR from the first image and the SHAPE from the second image. " +
			"Return one single flat icon, pure white background, no text."},
		{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(red)}},
		{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(blue)}},
	}}}}

	canonical, plan := liveProbePlan(t, request, model)
	refs := canonical.imageRefs()
	if len(refs) != 2 {
		t.Fatalf("canonical refs = %d, want 2", len(refs))
	}
	decoded, elapsed := liveProbePost(t, base, key, plan)
	if len(decoded.Data) != 1 {
		t.Fatalf("data = %d, want 1", len(decoded.Data))
	}
	t.Logf("upstream: %s; usage = %#v; revised = %q",
		elapsed, decoded.Extras["usage"], decoded.Data[0].Extras["revised_prompt"])
	liveProbeSave(t, "2-multiref-color-from-first-shape-from-second", decoded.Data[0])
}

// 用例 3：fileData 公网 URL 必须原样折进 image（零下载）且真的被上游用上。
func TestLiveProbeGeminiFileDataURL(t *testing.T) {
	base, key, model := liveProbeEnv(t)

	// 先造一张可引用的公网图（上游自己的 /files 链接，公开可取）。
	seed := &dto.GeminiChatRequest{Contents: []dto.GeminiChatContent{{Role: "user", Parts: []dto.GeminiPart{
		{Text: "a small red paper boat floating on calm blue water, photographic"},
	}}}}
	_, seedPlan := liveProbePlan(t, seed, model)
	seeded, _ := liveProbePost(t, base, key, seedPlan)
	if len(seeded.Data) != 1 || seeded.Data[0].URL == "" {
		t.Fatalf("seed generation returned no url: %#v", seeded.Data)
	}
	reference := seeded.Data[0].URL
	liveProbeSave(t, "3a-filedata-seed", seeded.Data[0])
	t.Logf("reference url = %s", reference)

	request := &dto.GeminiChatRequest{Contents: []dto.GeminiChatContent{{Role: "user", Parts: []dto.GeminiPart{
		{Text: "Keep the subject and composition, replace the background with a solid bright green."},
		{FileData: &dto.GeminiFileData{MimeType: "image/jpeg", FileUri: reference}},
	}}}}
	canonical, plan := liveProbePlan(t, request, model)
	if got := canonical.imageRefs(); len(got) != 1 || got[0] != reference {
		t.Fatalf("canonical refs = %#v, want the url untouched", got)
	}
	body := decodeGeminiPlanBody(t, plan.Body)
	if body["image"] != reference {
		t.Fatalf("upstream image = %#v, want the url forwarded (zero download)", body["image"])
	}

	decoded, elapsed := liveProbePost(t, base, key, plan)
	if len(decoded.Data) != 1 {
		t.Fatalf("data = %d, want 1", len(decoded.Data))
	}
	t.Logf("upstream: %s; usage = %#v; revised = %q",
		elapsed, decoded.Extras["usage"], decoded.Data[0].Extras["revised_prompt"])
	liveProbeSave(t, "3b-filedata-green-background", decoded.Data[0])
}

// liveProbeShape 画一张 256×256 的纯色图形参考图（白底），用来做「颜色 / 形状」
// 分工验收 —— 生成式模型对这类高对比几何图形几乎不会认错。
func liveProbeShape(t *testing.T, fill color.RGBA, shape string) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 256, 256))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	for y := range 256 {
		for x := range 256 {
			switch shape {
			case "circle":
				dx, dy := x-128, y-128
				if dx*dx+dy*dy <= 90*90 {
					canvas.Set(x, y, fill)
				}
			case "square":
				if x >= 40 && x <= 216 && y >= 40 && y <= 216 {
					canvas.Set(x, y, fill)
				}
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		t.Fatalf("encode %s: %v", shape, err)
	}
	return buf.Bytes()
}

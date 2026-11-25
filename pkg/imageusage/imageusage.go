// Package imageusage estimates the token usage of an image generation request
// when the upstream returns none.
//
// The estimate is a lower bound on purpose: an unknown size falls back to the
// smallest canvas, an unknown quality to the cheapest tier, and the result is
// floored. Charging slightly less than the upstream did is preferable to
// inventing usage for a request the upstream never metered.
//
// Only models with a published token table are estimated. A model outside the
// tables returns ok=false and the caller keeps its own placeholder, because a
// number on an unrelated provider's invoice would be fiction.
package imageusage

import (
	"encoding/json"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/tokenkit"
)

// Estimate is one request's token usage.
type Estimate struct {
	PromptTokens      int
	CompletionTokens  int
	ImageOutputTokens int
	// TextPromptTokens and InputImageTokens split PromptTokens.
	TextPromptTokens int
	InputImageTokens int
}

// baseImageSide is the canvas the published tables are quoted on. Every other
// size scales by area from it.
const baseImageSide = 1024

// openAIImageOutput is a generation model's output tokens for one
// 1024×1024 image at each quality tier.
type openAIImageOutput struct {
	low, medium, high int
}

// openAIImageTables maps a model prefix to its published output table.
//
// gpt-image-1 is the measured table from OpenAI's image guide. gpt-image-1-mini
// and gpt-image-1.5 share it: their per-image prices differ but the token
// counts per size and quality are the same. gpt-image-2 is a different scale
// (low 196, high 7,024 measured at 1024×1024); its medium tier is derived from
// the published per-image price ratio and is the one value here not measured.
var openAIImageTables = []struct {
	prefix string
	tokens openAIImageOutput
}{
	{"gpt-image-2", openAIImageOutput{low: 196, medium: 1763, high: 7024}},
	{"gpt-image-1", openAIImageOutput{low: 272, medium: 1056, high: 4160}},
}

// Input image costs, all quoted at 1024×1024. Sizes are not read: decoding the
// request's reference images would cost more than the under-estimate it fixes.
const (
	openAIInputImageTokens = 1025
	gemini3InputImageToken = 560
	gemini2InputImageToken = 258
)

const (
	gemini25FlashImageOutput = 1290
	gemini3ProImageOutput1K  = 1120
	gemini3ProImageOutput4K  = 2000
	gemini3ProImage4KMaxSide = 2048
)

// GeminiLowestImageOutputTokens is the cheapest published per-image output
// cost. Callers with no model name to look up use it rather than guessing a
// higher tier.
const GeminiLowestImageOutputTokens = gemini3ProImageOutput1K

// ForRequest estimates one image generation request. n is the number of images
// actually produced; inputImages is the number of reference images in the
// request. Both are clamped to at least one and zero respectively.
func ForRequest(model, prompt, size, quality string, n, inputImages int) (Estimate, bool) {
	if n < 1 {
		n = 1
	}
	if inputImages < 0 {
		inputImages = 0
	}

	var (
		output    int
		inputEach int
	)
	if tokens, ok := openAIOutputTokens(model, size, quality); ok {
		output = tokens
		inputEach = openAIInputImageTokens
	} else if tokens, ok := geminiOutputTokens(model, size); ok {
		output = tokens
		inputEach = geminiInputImageTokens(model)
	} else {
		return Estimate{}, false
	}

	textTokens := tokenkit.Estimate(model, prompt)
	inputTokens := inputEach * inputImages
	return Estimate{
		PromptTokens:      textTokens + inputTokens,
		CompletionTokens:  output * n,
		ImageOutputTokens: output * n,
		TextPromptTokens:  textTokens,
		InputImageTokens:  inputTokens,
	}, true
}

// Supported reports whether a model has a published token table. Callers with
// both an upstream alias and a public model name use it to pick the one the
// table knows before estimating.
func Supported(model string) bool {
	if _, ok := openAIOutputTokens(model, "", ""); ok {
		return true
	}
	_, ok := geminiOutputTokens(model, "")
	return ok
}

// Usage converts the estimate into the billing DTO. Both modality details are
// populated: a tiered expression reads them as img_i and img_o, and a missing
// img_o prices generated images as text.
func (e Estimate) Usage() *dto.Usage {
	return &dto.Usage{
		PromptTokens:     e.PromptTokens,
		CompletionTokens: e.CompletionTokens,
		TotalTokens:      e.PromptTokens + e.CompletionTokens,
		PromptTokensDetails: dto.InputTokenDetails{
			TextTokens:  e.TextPromptTokens,
			ImageTokens: e.InputImageTokens,
		},
		CompletionTokenDetails: dto.OutputTokenDetails{
			ImageTokens: e.ImageOutputTokens,
		},
	}
}

// openAIOutputTokens returns the output tokens one image costs on an OpenAI
// image model. Unknown models fall through to the Gemini tables, so the caller
// gets one answer rather than two conflicting ones.
func openAIOutputTokens(model, size, quality string) (int, bool) {
	lower := strings.ToLower(model)
	var table *openAIImageOutput
	for i := range openAIImageTables {
		if strings.HasPrefix(lower, openAIImageTables[i].prefix) {
			table = &openAIImageTables[i].tokens
			break
		}
	}
	if table == nil {
		return 0, false
	}

	w, h := canvasSize(size)
	base := table.low
	switch normalizeQuality(quality) {
	case "medium":
		base = table.medium
	case "high":
		base = table.high
	}
	// Floor keeps the estimate under the upstream count when the canvas is not
	// a whole multiple of the base one.
	return base * w * h / (baseImageSide * baseImageSide), true
}

// geminiOutputTokens returns the output tokens one image costs on a Gemini
// image model. 2.5 Flash Image charges a flat 1290 regardless of aspect ratio
// or resolution; 3 Pro Image charges by output tier.
func geminiOutputTokens(model, size string) (int, bool) {
	lower := strings.ToLower(model)
	// "imagen" contains "image"; Imagen prices per image, not per token, so it
	// must not be captured by the Gemini image rule.
	if strings.Contains(lower, "imagen") {
		return 0, false
	}
	// nano-banana is Google's alias for the Gemini image family; the upstream
	// name a channel maps to (google/gemini-nano-banana-2.1) carries no "image".
	banana := strings.Contains(lower, "nano-banana")
	if !strings.Contains(lower, "gemini") && !banana {
		return 0, false
	}
	if !strings.Contains(lower, "image") && !banana {
		return 0, false
	}
	if strings.Contains(lower, "gemini-3") || banana {
		w, h := canvasSize(size)
		if max(w, h) > gemini3ProImage4KMaxSide {
			return gemini3ProImageOutput4K, true
		}
		return gemini3ProImageOutput1K, true
	}
	// Gemini 2.x image models, and any Gemini image model without a version,
	// take the cheapest published tier.
	return gemini25FlashImageOutput, true
}

// GeminiImageOutputTokens is geminiOutputTokens for callers outside this
// package (the chat entry, which has no image request to describe).
func GeminiImageOutputTokens(model, size string) int {
	tokens, ok := geminiOutputTokens(model, size)
	if !ok {
		return 0
	}
	return tokens
}

// geminiInputImageTokens returns per-image input cost. Gemini 3 models bill a
// fixed 560 per reference image, 2.x models a 258-token tile.
func geminiInputImageTokens(model string) int {
	lower := strings.ToLower(model)
	if strings.Contains(lower, "gemini-3") || strings.Contains(lower, "nano-banana") {
		return gemini3InputImageToken
	}
	return gemini2InputImageToken
}

// canvasSize parses an OpenAI size such as "1024x1536". An empty or "auto"
// size means the smallest canvas, which is the lower bound the estimate wants.
func canvasSize(size string) (int, int) {
	w, h, ok := strings.Cut(strings.ToLower(strings.TrimSpace(size)), "x")
	if !ok {
		return baseImageSide, baseImageSide
	}
	width, widthOK := parsePositiveInt(w)
	height, heightOK := parsePositiveInt(h)
	if !widthOK || !heightOK {
		return baseImageSide, baseImageSide
	}
	return width, height
}

func parsePositiveInt(value string) (int, bool) {
	n := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
		if n > 65535 {
			return 0, false
		}
	}
	return n, n > 0
}

// normalizeQuality maps a request's quality onto a table tier. OpenAI's
// default is "auto", which usually resolves to high; charging the low tier is
// the deliberate under-estimate.
func normalizeQuality(quality string) string {
	switch strings.ToLower(strings.TrimSpace(quality)) {
	case "high":
		return "high"
	case "medium":
		return "medium"
	default:
		return "low"
	}
}

// CountInputImages returns how many reference images the request carries.
// Images may be a single string (a URL or data URI) or an array of them.
func CountInputImages(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if strings.TrimSpace(single) == "" {
			return 0
		}
		return 1
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err == nil {
		return len(list)
	}
	return 0
}

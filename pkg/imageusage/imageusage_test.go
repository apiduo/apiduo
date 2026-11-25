package imageusage

import (
	"encoding/json"
	"testing"
)

func TestOpenAIImageOutputFollowsPublishedTable(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		size    string
		quality string
		n       int
		want    int
	}{
		{"square low", "gpt-image-1", "1024x1024", "low", 1, 272},
		{"square medium", "gpt-image-1", "1024x1024", "medium", 1, 1056},
		{"square high", "gpt-image-1", "1024x1024", "high", 1, 4160},
		{"portrait low", "gpt-image-1", "1024x1536", "low", 1, 408},
		{"portrait medium", "gpt-image-1", "1024x1536", "medium", 1, 1584},
		{"portrait high", "gpt-image-1", "1024x1536", "high", 1, 6240},
		{"two images", "gpt-image-1", "1024x1024", "high", 2, 8320},
		{"mini shares the table", "gpt-image-1-mini", "1024x1024", "medium", 1, 1056},
		{"1.5 shares the table", "gpt-image-1.5", "1024x1024", "high", 1, 4160},
		{"gpt-image-2 has its own scale", "gpt-image-2", "1024x1024", "low", 1, 196},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			estimate, ok := ForRequest(tc.model, "", tc.size, tc.quality, tc.n, 0)
			if !ok {
				t.Fatalf("ForRequest(%q) not estimated", tc.model)
			}
			if estimate.ImageOutputTokens != tc.want {
				t.Fatalf("image output tokens = %d, want %d", estimate.ImageOutputTokens, tc.want)
			}
			if estimate.CompletionTokens != estimate.ImageOutputTokens {
				t.Fatalf("completion = %d, want image output %d", estimate.CompletionTokens, estimate.ImageOutputTokens)
			}
		})
	}
}

// The estimate is a lower bound: an unreported quality and size must resolve to
// the smallest canvas and the cheapest tier, never to the model's default.
func TestUnknownSizeAndQualityTakeTheLowerBound(t *testing.T) {
	empty, ok := ForRequest("gpt-image-1", "", "", "", 1, 0)
	if !ok {
		t.Fatal("not estimated")
	}
	explicit, ok := ForRequest("gpt-image-1", "", "1024x1024", "low", 1, 0)
	if !ok {
		t.Fatal("not estimated")
	}
	if empty.ImageOutputTokens != explicit.ImageOutputTokens {
		t.Fatalf("defaults = %d, want the low tier %d", empty.ImageOutputTokens, explicit.ImageOutputTokens)
	}
	if empty.ImageOutputTokens != 272 {
		t.Fatalf("low tier = %d, want 272", empty.ImageOutputTokens)
	}
}

func TestGeminiImageOutput(t *testing.T) {
	tests := []struct {
		name  string
		model string
		size  string
		want  int
	}{
		{"2.5 flash image is flat", "gemini-2.5-flash-image", "1024x1024", 1290},
		{"2.5 flash image portrait", "gemini-2.5-flash-image", "832x1248", 1290},
		{"3 pro 1K", "gemini-3-pro-image-preview", "1024x1024", 1120},
		{"nano-banana alias", "google/gemini-nano-banana-2.1", "1024x1024", 1120},
		{"nano-banana 4K", "google/gemini-nano-banana-2.1", "4096x4096", 2000},
		{"3 pro 2K", "gemini-3-pro-image-preview", "2048x2048", 1120},
		{"3 pro 4K", "gemini-3-pro-image-preview", "4096x4096", 2000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			estimate, ok := ForRequest(tc.model, "", tc.size, "", 1, 0)
			if !ok {
				t.Fatalf("ForRequest(%q) not estimated", tc.model)
			}
			if estimate.ImageOutputTokens != tc.want {
				t.Fatalf("image output tokens = %d, want %d", estimate.ImageOutputTokens, tc.want)
			}
		})
	}
}

func TestUnlistedModelsAreNotEstimated(t *testing.T) {
	for _, model := range []string{
		"imagen-3.0-generate-002",
		"doubao-seedream-4-0",
		"flux-3-image",
		"",
	} {
		if _, ok := ForRequest(model, "a cat", "1024x1024", "high", 1, 0); ok {
			t.Fatalf("%q must not be estimated", model)
		}
	}
}

func TestPromptIncludesTextAndReferenceImages(t *testing.T) {
	textOnly, ok := ForRequest("gpt-image-1", "a cat on a mat", "1024x1024", "low", 1, 0)
	if !ok {
		t.Fatal("not estimated")
	}
	if textOnly.TextPromptTokens == 0 {
		t.Fatal("text prompt tokens not counted")
	}
	if textOnly.InputImageTokens != 0 {
		t.Fatalf("input image tokens = %d, want 0", textOnly.InputImageTokens)
	}

	withImages, ok := ForRequest("gpt-image-1", "a cat on a mat", "1024x1024", "low", 1, 2)
	if !ok {
		t.Fatal("not estimated")
	}
	if withImages.InputImageTokens != 2*openAIInputImageTokens {
		t.Fatalf("input image tokens = %d, want %d", withImages.InputImageTokens, 2*openAIInputImageTokens)
	}
	if withImages.PromptTokens != textOnly.TextPromptTokens+withImages.InputImageTokens {
		t.Fatalf("prompt = %d, want text %d + images %d", withImages.PromptTokens, textOnly.TextPromptTokens, withImages.InputImageTokens)
	}
}

func TestSupported(t *testing.T) {
	for _, model := range []string{"gpt-image-1", "gpt-image-1-mini", "gpt-image-2", "gemini-2.5-flash-image", "gemini-3-pro-image-preview", "google/gemini-nano-banana-2.1"} {
		if !Supported(model) {
			t.Fatalf("%q must be supported", model)
		}
	}
	for _, model := range []string{"", "imagen-3.0-generate-002", "doubao-seedream-4-0", "flux-3-image"} {
		if Supported(model) {
			t.Fatalf("%q must not be supported", model)
		}
	}
}

func TestUsageCarriesModalityDetails(t *testing.T) {
	estimate, ok := ForRequest("gpt-image-1", "a cat", "1024x1024", "medium", 2, 1)
	if !ok {
		t.Fatal("not estimated")
	}
	usage := estimate.Usage()
	if usage.PromptTokens != estimate.PromptTokens || usage.CompletionTokens != estimate.CompletionTokens {
		t.Fatalf("usage = %+v, estimate = %+v", usage, estimate)
	}
	if usage.TotalTokens != usage.PromptTokens+usage.CompletionTokens {
		t.Fatalf("total = %d, want prompt+completion", usage.TotalTokens)
	}
	if usage.PromptTokensDetails.ImageTokens != openAIInputImageTokens {
		t.Fatalf("input image tokens = %d, want %d", usage.PromptTokensDetails.ImageTokens, openAIInputImageTokens)
	}
	if usage.PromptTokensDetails.TextTokens == 0 {
		t.Fatal("text prompt tokens missing")
	}
	if usage.CompletionTokenDetails.ImageTokens != 2*1056 {
		t.Fatalf("img_o = %d, want %d", usage.CompletionTokenDetails.ImageTokens, 2*1056)
	}
}

func TestCountInputImages(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{"absent", "", 0},
		{"single", `"https://example.com/a.png"`, 1},
		{"blank", `"  "`, 0},
		{"array", `["a.png","b.png"]`, 2},
		{"object", `{"url":"a.png"}`, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CountInputImages(json.RawMessage(tc.raw)); got != tc.want {
				t.Fatalf("CountInputImages(%s) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

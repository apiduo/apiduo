package gemini

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

func TestImagePredictUsagePrefersUpstreamMetadata(t *testing.T) {
	estimate := &dto.Usage{PromptTokens: 10, CompletionTokens: 1290, TotalTokens: 1300}
	metadata := &dto.GeminiUsageMetadata{
		PromptTokenCount:     7,
		CandidatesTokenCount: 1120,
		TotalTokenCount:      1649,
		ThoughtsTokenCount:   526,
		CandidatesTokensDetails: []dto.GeminiPromptTokensDetails{
			{Modality: "IMAGE", TokenCount: 1120},
		},
	}

	usage := imagePredictUsage(metadata, estimate, 0)
	if usage == estimate {
		t.Fatal("upstream metadata must win over the local estimate")
	}
	if usage.PromptTokens != 7 || usage.CompletionTokens != 1646 || usage.TotalTokens != 1649 {
		t.Fatalf("usage = %+v, want prompt 7 / completion 1646 / total 1649", usage)
	}
	if usage.CompletionTokenDetails.ImageTokens != 1120 {
		t.Fatalf("image output tokens = %d, want 1120", usage.CompletionTokenDetails.ImageTokens)
	}
}

func TestImagePredictUsageFallsBackToEstimate(t *testing.T) {
	estimate := &dto.Usage{
		PromptTokens:           12,
		CompletionTokens:       1290,
		TotalTokens:            1302,
		CompletionTokenDetails: dto.OutputTokenDetails{ImageTokens: 1290},
	}

	usage := imagePredictUsage(nil, estimate, 99)
	if usage != estimate {
		t.Fatalf("usage = %+v, want the estimate", usage)
	}
}

// A predict response with no metadata and no published table keeps the
// single-unit placeholder: it satisfies hasBillableUsage without inventing a
// token count for an unknown model.
func TestImagePredictUsageKeepsSingleUnitPlaceholder(t *testing.T) {
	usage := imagePredictUsage(nil, nil, 0)
	if usage.PromptTokens != 1 || usage.CompletionTokens != 0 || usage.TotalTokens != 1 {
		t.Fatalf("usage = %+v, want the single-unit placeholder", usage)
	}
}

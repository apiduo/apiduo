package relay

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// hasUpstreamImageUsage separates a real usage object from the single-unit
// placeholder. A real image request never costs one token, so treating 1/0/1 as
// unreported costs at most one token of accuracy and prevents an estimated
// number from being overwritten by a placeholder.
func TestHasUpstreamImageUsage(t *testing.T) {
	tests := []struct {
		name  string
		usage *dto.Usage
		want  bool
	}{
		{"nil", nil, false},
		{"empty", &dto.Usage{}, false},
		{"placeholder", &dto.Usage{PromptTokens: 1, TotalTokens: 1}, false},
		{"gemini image", &dto.Usage{PromptTokens: 3, CompletionTokens: 1290, TotalTokens: 1293}, true},
		{"openai image", &dto.Usage{PromptTokens: 21, CompletionTokens: 196, TotalTokens: 217}, true},
		{"completion only", &dto.Usage{CompletionTokens: 1120}, true},
		{"prompt above placeholder", &dto.Usage{PromptTokens: 2, TotalTokens: 2}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasUpstreamImageUsage(tc.usage); got != tc.want {
				t.Fatalf("hasUpstreamImageUsage(%+v) = %v, want %v", tc.usage, got, tc.want)
			}
		})
	}
}

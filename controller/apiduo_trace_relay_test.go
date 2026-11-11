package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
)

// An images request has no `messages` and answers with `data[]`. Bucketing it
// under "chat" made the drawer label it `chat flux-3-image` right next to that
// body, which reads as "the chat endpoint answered in the wrong shape".
func TestApiduoTraceOperationSeparatesImageFromChat(t *testing.T) {
	cases := []struct {
		format types.RelayFormat
		want   string
	}{
		{types.RelayFormatOpenAI, "chat"},
		{types.RelayFormatClaude, "chat"},
		{types.RelayFormatGemini, "chat"},
		{types.RelayFormatOpenAIResponses, "chat"},
		{types.RelayFormatOpenAIImage, "image"},
	}
	for _, c := range cases {
		if got := apiduoTraceOperation(c.format); got != c.want {
			t.Errorf("apiduoTraceOperation(%q) = %q, want %q", c.format, got, c.want)
		}
	}
}

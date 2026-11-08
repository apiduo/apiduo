package trace

import "encoding/json"

// OpenAI chat/completions stream parser.
//
// Frame shape: {"choices":[{"delta":{"content":"…","tool_calls":[{"index":0,
// "function":{"name":"…","arguments":"{…"}}]},"finish_reason":"stop"}]}
// One line per frame; blank lines and [DONE] are skipped by frameLines.
type openAIParser struct{}

func (openAIParser) Parse(frames string) (ParsedOutput, bool) {
	var text string
	var finish string
	fragments := map[int]*ToolCall{}
	recognized := false
	for line := range frameLines(frames) {
		var frame struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int `json:"index"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				// Non-stream completions use message where streams use
				// delta. Same fields, so one parser serves both carriers.
				Message struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int `json:"index"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Error any `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			continue
		}
		if frame.Error != nil && len(frame.Choices) == 0 {
			// Error frames are not model output; leave them to the raw view.
			continue
		}
		if len(frame.Choices) == 0 {
			continue
		}
		recognized = true
		choice := frame.Choices[0]
		text += choice.Delta.Content + choice.Message.Content
		if choice.FinishReason != "" {
			finish = choice.FinishReason
		}
		for _, tc := range append(choice.Delta.ToolCalls, choice.Message.ToolCalls...) {
			acc, ok := fragments[tc.Index]
			if !ok {
				acc = &ToolCall{}
				fragments[tc.Index] = acc
			}
			if tc.Function.Name != "" {
				acc.Name = tc.Function.Name
			}
			acc.Arguments += tc.Function.Arguments
		}
	}
	if !recognized {
		return ParsedOutput{}, false
	}
	return ParsedOutput{
		Text:         text,
		ToolCalls:    mergeToolCallFragments(fragments),
		FinishReason: finish,
	}, true
}

func init() { RegisterParser("openai", openAIParser{}) }

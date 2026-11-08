package trace

import "encoding/json"

// Google Gemini stream parser.
//
// Frames: {"candidates":[{"content":{"parts":[{"text":"…"}]},
// "finishReason":"STOP"}]}. Streaming may also wrap several parts per frame,
// and non-text parts (inlineData) are skipped.
type geminiParser struct{}

func (geminiParser) Parse(frames string) (ParsedOutput, bool) {
	var text string
	var finish string
	recognized := false
	for line := range frameLines(frames) {
		var frame struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
		}
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			continue
		}
		if len(frame.Candidates) == 0 {
			continue
		}
		recognized = true
		cand := frame.Candidates[0]
		for _, part := range cand.Content.Parts {
			text += part.Text
		}
		if cand.FinishReason != "" {
			finish = cand.FinishReason
		}
	}
	if !recognized {
		return ParsedOutput{}, false
	}
	return ParsedOutput{Text: text, FinishReason: finish}, true
}

func init() { RegisterParser("gemini", geminiParser{}) }

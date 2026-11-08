package trace

import "encoding/json"

// Anthropic Messages stream parser.
//
// Frames: content_block_start (tool_use name) → content_block_delta
// (text_delta.text or input_json_delta.partial_json) → message_delta
// (stop_reason). Other event types (ping, message_start) are ignored.
type claudeParser struct{}

func (claudeParser) Parse(frames string) (ParsedOutput, bool) {
	var text string
	var finish string
	var toolCalls []ToolCall
	var currentTool *ToolCall
	recognized := false
	for line := range frameLines(frames) {
		var frame struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			ContentBlock struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content_block"`
		}
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			continue
		}
		switch frame.Type {
		case "content_block_start":
			if frame.ContentBlock.Type == "tool_use" {
				toolCalls = append(toolCalls, ToolCall{Name: frame.ContentBlock.Name})
				currentTool = &toolCalls[len(toolCalls)-1]
				recognized = true
			}
		case "content_block_delta":
			recognized = true
			switch frame.Delta.Type {
			case "text_delta":
				text += frame.Delta.Text
			case "input_json_delta":
				if currentTool != nil {
					currentTool.Arguments += frame.Delta.PartialJSON
				}
			}
		case "message_delta":
			recognized = true
			if frame.Delta.StopReason != "" {
				finish = frame.Delta.StopReason
			}
		}
	}
	if !recognized {
		return ParsedOutput{}, false
	}
	return ParsedOutput{Text: text, ToolCalls: toolCalls, FinishReason: finish}, true
}

func init() { RegisterParser("claude", claudeParser{}) }

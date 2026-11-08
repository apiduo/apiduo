package trace

import (
	"strings"
)

// Frame parsing turns accumulated SSE frame text into a readable model
// answer. It runs in the flush worker, never on the request path.
//
// Extension point: a new upstream wire format is one file plus a
// RegisterParser call in its init — no edits to the exporter, payload
// builder, or config. A format nobody registered keeps its raw frames,
// which is the documented fallback ("认不出的格式原样保留").

// ParsedOutput is a format-neutral view of a streamed answer.
type ParsedOutput struct {
	Text         string
	ToolCalls    []ToolCall
	FinishReason string
}

// ToolCall is a tool invocation extracted from a stream.
type ToolCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// FrameParser reads raw frame text (frames joined by newlines) and returns
// the assembled output. ok=false means "this input is not my format" —
// callers then keep the raw text.
type FrameParser interface {
	Parse(frames string) (ParsedOutput, bool)
}

var parserRegistry = map[string]FrameParser{}

// RegisterParser binds a relay format ("openai", "claude", "gemini", ...)
// to a parser. Called from parser init functions.
func RegisterParser(format string, p FrameParser) {
	if format == "" || p == nil {
		return
	}
	parserRegistry[format] = p
}

// parseOutput looks up the parser for a relay format and runs it. ok=false
// covers three cases that all mean "keep raw": no parser registered, parser
// declined the input, and parse produced nothing.
func parseOutput(format, frames string) (ParsedOutput, bool) {
	if frames == "" {
		return ParsedOutput{}, false
	}
	p, ok := parserRegistry[format]
	if !ok {
		return ParsedOutput{}, false
	}
	out, ok := p.Parse(frames)
	if !ok {
		return ParsedOutput{}, false
	}
	// A finish_reason on its own carries no conversational content: claiming
	// it produced the useless payload {"finish_reason":"stop","text":""}
	// while the raw body held the whole answer (hit by non-stream responses,
	// which this parser used to misread as a stream with no deltas).
	if out.Text == "" && len(out.ToolCalls) == 0 {
		return ParsedOutput{}, false
	}
	return out, true
}

// frameLines yields non-empty frame lines. SSE payload frames are JSON
// objects one per line; blank lines and the "[DONE]" sentinel are skipped.
func frameLines(frames string) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for line := range strings.SplitSeq(frames, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || line == "data: [DONE]" || line == "[DONE]" {
				continue
			}
			line = strings.TrimPrefix(line, "data: ")
			if !yield(line) {
				return
			}
		}
	}
}

// mergeToolCallFragments assembles OpenAI-style streamed tool calls, where
// name arrives once and arguments arrive as JSON fragments.
func mergeToolCallFragments(fragments map[int]*ToolCall) []ToolCall {
	if len(fragments) == 0 {
		return nil
	}
	// Indexes are dense in practice; keep first-seen order for stability.
	out := make([]ToolCall, 0, len(fragments))
	for i := range len(fragments) {
		if tc, ok := fragments[i]; ok {
			out = append(out, *tc)
		}
	}
	return out
}

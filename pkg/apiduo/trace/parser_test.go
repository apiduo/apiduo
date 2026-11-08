package trace

import (
	"strings"
	"testing"
)

const openAIFrames = `{"choices":[{"delta":{"role":"assistant","content":""}}]}
{"choices":[{"delta":{"content":"你好"}}]}
{"choices":[{"delta":{"content":"，世界"}}]}
{"choices":[{"delta":{},"finish_reason":"stop"}]}
data: [DONE]`

func TestOpenAIParser(t *testing.T) {
	out, ok := openAIParser{}.Parse(openAIFrames)
	if !ok {
		t.Fatal("openai frames must be recognized")
	}
	if out.Text != "你好，世界" {
		t.Fatalf("text: %q", out.Text)
	}
	if out.FinishReason != "stop" {
		t.Fatalf("finish: %q", out.FinishReason)
	}
}

func TestOpenAIParserToolCallFragments(t *testing.T) {
	frames := strings.Join([]string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"weather","arguments":"{\"ci"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ty\":\"北京\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	}, "\n")
	out, ok := openAIParser{}.Parse(frames)
	if !ok {
		t.Fatal("tool-call frames must parse")
	}
	if len(out.ToolCalls) != 1 {
		t.Fatalf("tool calls: %d", len(out.ToolCalls))
	}
	if out.ToolCalls[0].Name != "weather" {
		t.Fatalf("name: %q", out.ToolCalls[0].Name)
	}
	if out.ToolCalls[0].Arguments != `{"city":"北京"}` {
		t.Fatalf("arguments merged wrong: %q", out.ToolCalls[0].Arguments)
	}
	if out.FinishReason != "tool_calls" {
		t.Fatalf("finish: %q", out.FinishReason)
	}
}

func TestClaudeParser(t *testing.T) {
	frames := strings.Join([]string{
		`{"type":"message_start","message":{"id":"m"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		`{"type":"message_stop"}`,
	}, "\n")
	out, ok := claudeParser{}.Parse(frames)
	if !ok {
		t.Fatal("claude frames must parse")
	}
	if out.Text != "Hello world" {
		t.Fatalf("text: %q", out.Text)
	}
	if out.FinishReason != "end_turn" {
		t.Fatalf("finish: %q", out.FinishReason)
	}
}

func TestClaudeParserToolUse(t *testing.T) {
	frames := strings.Join([]string{
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","name":"search"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"go\"}"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
	}, "\n")
	out, ok := claudeParser{}.Parse(frames)
	if !ok || len(out.ToolCalls) != 1 {
		t.Fatalf("tool_use not parsed: %+v", out)
	}
	if out.ToolCalls[0].Arguments != `{"q":"go"}` {
		t.Fatalf("args: %q", out.ToolCalls[0].Arguments)
	}
}

func TestGeminiParser(t *testing.T) {
	frames := strings.Join([]string{
		`{"candidates":[{"content":{"parts":[{"text":"Hel"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"lo"},{"text":"!"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":""}]},"finishReason":"STOP"}]}`,
	}, "\n")
	out, ok := geminiParser{}.Parse(frames)
	if !ok {
		t.Fatal("gemini frames must parse")
	}
	if out.Text != "Hello!" {
		t.Fatalf("text: %q", out.Text)
	}
	if out.FinishReason != "STOP" {
		t.Fatalf("finish: %q", out.FinishReason)
	}
}

func TestParserRejectsUnknownShape(t *testing.T) {
	if _, ok := (openAIParser{}).Parse("not json at all"); ok {
		t.Fatal("garbage must not be claimed by the openai parser")
	}
	if _, ok := (claudeParser{}).Parse(`{"foo":1}`); ok {
		t.Fatal("unrelated json must not be claimed")
	}
	if _, ok := parseOutput("unknown-format", `{"x":1}`); ok {
		t.Fatal("no parser registered for unknown format")
	}
	if _, ok := parseOutput("openai", ""); ok {
		t.Fatal("empty frames must not parse")
	}
}

// A registry-driven lookup keeps the extension path honest: a new format is
// a RegisterParser call, nothing else.
func TestParserRegistry(t *testing.T) {
	type stubParser struct{ called bool }
	prev, existed := parserRegistry["test-format"]
	RegisterParser("test-format", stubFn(func() (ParsedOutput, bool) {
		return ParsedOutput{Text: "x"}, true
	}))
	defer func() {
		if existed {
			parserRegistry["test-format"] = prev
		} else {
			delete(parserRegistry, "test-format")
		}
	}()
	if _, ok := parseOutput("test-format", "anything"); !ok {
		t.Fatal("registered parser must be used")
	}
	_ = stubParser{}
}

type stubFn func() (ParsedOutput, bool)

func (f stubFn) Parse(string) (ParsedOutput, bool) { return f() }

// A non-stream completion must be parsed, not misread as an empty stream: it
// carries message.content where a stream carries delta.content. Before this,
// Langfuse showed {"finish_reason":"stop","text":""} for every non-stream call.
func TestOpenAIParserHandlesCompletionShape(t *testing.T) {
	body := `{"id":"chatcmpl-1","choices":[{"index":0,"message":{"role":"assistant","content":"北京明天多云转晴。","tool_calls":[{"index":0,"function":{"name":"weather","arguments":"{\"city\":\"北京\"}"}}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`
	got, ok := parseOutput("openai", body)
	if !ok {
		t.Fatal("completion shape must be recognised")
	}
	if !strings.Contains(got.Text, "北京明天多云转晴") {
		t.Fatalf("text: %q", got.Text)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "weather" {
		t.Fatalf("tool calls: %+v", got.ToolCalls)
	}
	if got.FinishReason != "stop" {
		t.Fatalf("finish: %q", got.FinishReason)
	}
}

// Only a finish_reason and no content is not a parse: fall back to raw so the
// operator sees the real body instead of an empty text field.
func TestParserDoesNotClaimEmptyOutput(t *testing.T) {
	if _, ok := parseOutput("openai", `{"choices":[{"delta":{},"finish_reason":"stop"}]}`); ok {
		t.Fatal("finish-only frame set must not be claimed")
	}
}

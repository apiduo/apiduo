package trace

import (
	"strings"
	"testing"
)

func TestRedactAPIKey(t *testing.T) {
	in := `curl -H "Authorization: Bearer sk-proj-AbCdEf0123456789xyz" https://api`
	out := Redact(in)
	if strings.Contains(out, "sk-proj-AbCdEf0123456789xyz") {
		t.Fatalf("api key leaked: %q", out)
	}
	if !strings.Contains(out, "[REDACTED:api_key]") {
		t.Fatalf("missing marker: %q", out)
	}
}

func TestRedactChinesePII(t *testing.T) {
	out := Redact("手机号 13812345678，身份证 110101199003071234，邮箱 a.b@example.com")
	for _, secret := range []string{"13812345678", "110101199003071234", "a.b@example.com"} {
		if strings.Contains(out, secret) {
			t.Fatalf("%s leaked: %q", secret, out)
		}
	}
	if !strings.Contains(out, "[REDACTED:phone]") ||
		!strings.Contains(out, "[REDACTED:id_card]") ||
		!strings.Contains(out, "[REDACTED:email]") {
		t.Fatalf("markers missing: %q", out)
	}
}

func TestRedactKeepsModelTextIntact(t *testing.T) {
	// Ordinary prose and short numbers must survive — over-redaction destroys
	// the product's own value.
	in := "第 3 条建议：把预算从 1000 提到 2500，sk-learn 的写法参考文档。"
	if got := Redact(in); got != in {
		t.Fatalf("plain text changed: %q", got)
	}
}

func TestRedactSwitch(t *testing.T) {
	conf := &Config{Redact: false}
	in := "sk-proj-AbCdEf0123456789xyz"
	if got := redactForExport(conf, in); got != in {
		t.Fatal("Redact=false must pass text through")
	}
	conf.Redact = true
	if got := redactForExport(conf, in); strings.Contains(got, "sk-proj-") {
		t.Fatal("Redact=true must scrub")
	}
}

// The parse-then-redact pipeline is what actually leaves the process: a tool
// argument carrying a key must be scrubbed inside the assembled JSON.
func TestFormatOutputParsesThenRedacts(t *testing.T) {
	conf := &Config{Redact: true, BodyMode: BodyHeadTail}
	sn := &Snapshot{
		Format:     "openai",
		HasContent: true,
		Output: []byte(strings.Join([]string{
			`{"choices":[{"delta":{"content":"key is sk-proj-AbCdEf0123456789xyz"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		}, "\n")),
	}
	text := redactForExport(conf, formatOutput(sn))
	if strings.Contains(text, "sk-proj-AbCdEf0123456789xyz") {
		t.Fatalf("key leaked through parse pipeline: %q", text)
	}
	if !strings.Contains(text, "finish_reason") {
		t.Fatalf("parsed view lost: %q", text)
	}
}

func TestFormatOutputFallsBackToRaw(t *testing.T) {
	sn := &Snapshot{
		Format:     "unknown-upstream",
		HasContent: true,
		Output:     []byte("raw frame text"),
	}
	if got := formatOutput(sn); got != "raw frame text" {
		t.Fatalf("unknown format must keep raw: %q", got)
	}
}

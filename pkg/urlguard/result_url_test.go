package urlguard

import (
	"strings"
	"testing"
)

func TestValidateTaskResultURL(t *testing.T) {
	accepted := []string{
		"https://cdn.example.com/moss/prod/x.png",
		"http://127.0.0.1:9000/bucket/key.png?sig=abc",
		"  https://cdn.example.com/x.png  ",
	}
	for _, raw := range accepted {
		if _, err := ValidateTaskResultURL(raw); err != nil {
			t.Errorf("%q should be accepted: %v", raw, err)
		}
	}

	rejected := map[string]string{
		"":                                     "empty",
		"javascript:alert(document.cookie)":    "a scheme the client would execute",
		"data:image/png;base64,AAAA":           "inline bytes, not an address",
		"file:///etc/passwd":                   "local file",
		"/relative/result.png":                 "not absolute",
		"https://user:pass@cdn.example.com/x":  "embedded credentials",
		"https://" + strings.Repeat("a", 9000): "over the length ceiling",
	}
	for raw, why := range rejected {
		if _, err := ValidateTaskResultURL(raw); err == nil {
			t.Errorf("%q must be rejected: %s", raw, why)
		}
	}
}

// 裁剪空白后返回，避免把客户端的首尾空格存进任务行。
func TestValidateTaskResultURLTrims(t *testing.T) {
	got, err := ValidateTaskResultURL("  https://cdn.example.com/x.png\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://cdn.example.com/x.png" {
		t.Fatalf("got %q", got)
	}
}

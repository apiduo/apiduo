package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// Reading a log and reading the conversation behind it are separate grants.
// StripLogTraceIDs is what enforces the second one on the response payload, so
// it must remove only trace_id and leave the rest of the row intact.
func TestStripLogTraceIDs(t *testing.T) {
	withID := `{"admin_info":{"trace_id":"abc123","request_policy":[{"attempt":1}]},"cache_tokens":0}`
	onlyID := `{"admin_info":{"trace_id":"abc123"}}`
	noID := `{"admin_info":{"request_policy":[]},"cache_tokens":5}`
	notJSON := `not-json`

	logs := []*Log{
		{Other: withID},
		{Other: onlyID},
		{Other: noID},
		{Other: notJSON},
		{Other: ""},
	}
	StripLogTraceIDs(logs)

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(logs[0].Other), &parsed); err != nil {
		t.Fatalf("row 0 must stay valid JSON: %v (%s)", err, logs[0].Other)
	}
	var admin map[string]json.RawMessage
	if err := json.Unmarshal(parsed["admin_info"], &admin); err != nil {
		t.Fatalf("admin_info must stay valid JSON: %v", err)
	}
	if _, ok := admin["trace_id"]; ok {
		t.Fatalf("trace_id must be stripped: %s", logs[0].Other)
	}
	if _, ok := admin["request_policy"]; !ok {
		t.Fatalf("other admin_info fields must survive: %s", logs[0].Other)
	}
	if _, ok := parsed["cache_tokens"]; !ok {
		t.Fatalf("top-level fields must survive: %s", logs[0].Other)
	}

	if strings.Contains(logs[1].Other, "admin_info") {
		t.Fatalf("an emptied admin_info must be removed, not left as {}: %s", logs[1].Other)
	}
	if logs[2].Other != noID {
		t.Fatalf("rows without trace_id must be untouched: %s", logs[2].Other)
	}
	if logs[3].Other != notJSON {
		t.Fatalf("malformed rows must be left alone, not blanked: %s", logs[3].Other)
	}
	if logs[4].Other != "" {
		t.Fatalf("empty rows stay empty: %q", logs[4].Other)
	}
}

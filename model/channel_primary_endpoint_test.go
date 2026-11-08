package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
)

func TestNormalizePrimaryDerivedRoutes(t *testing.T) {
	channel := &Channel{OtherSettings: `{
		"primary_endpoint": " /v1/images/generations ",
		"keep_me": {"a": 1},
		"advanced_custom": {"advanced_routes": [
			{"incoming_path": "/v1/images/edits", "upstream_path": "/v1/images/generations"},
			{"incoming_path": "/v1/chat/completions", "upstream_path": "/v1/chat/completions"},
			{"incoming_path": "/v1/images/generations"}
		]}
	}`}

	if err := channel.NormalizePrimaryDerivedRoutes(); err != nil {
		t.Fatalf("normalize: %v", err)
	}

	var settings map[string]any
	if err := common.UnmarshalJsonStr(channel.OtherSettings, &settings); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	custom := settings["advanced_custom"].(map[string]any)
	routes := custom["advanced_routes"].([]any)

	follows := routes[0].(map[string]any)
	if value, _ := follows["upstream_path"].(string); value != "" {
		t.Fatalf("route equal to primary must be emptied, got %q", value)
	}
	explicit := routes[1].(map[string]any)
	if explicit["upstream_path"] != "/v1/chat/completions" {
		t.Fatalf("explicit upstream must be kept, got %#v", explicit["upstream_path"])
	}
	empty := routes[2].(map[string]any)
	if empty["upstream_path"] != nil && empty["upstream_path"] != "" {
		t.Fatalf("already empty route must stay empty, got %#v", empty["upstream_path"])
	}
	if _, ok := settings["keep_me"]; !ok {
		t.Fatal("unknown settings keys must survive normalization")
	}

	// 幂等：再跑一次不应再改。
	before := channel.OtherSettings
	if err := channel.NormalizePrimaryDerivedRoutes(); err != nil {
		t.Fatalf("second normalize: %v", err)
	}
	if channel.OtherSettings != before {
		t.Fatal("normalization must be idempotent")
	}
}

func TestNormalizePrimaryDerivedRoutesSkipsWithoutPrimary(t *testing.T) {
	original := `{"advanced_custom": {"advanced_routes": [{"incoming_path": "/v1/images/edits", "upstream_path": "/v1/images/generations"}]}}`
	channel := &Channel{OtherSettings: original}
	if err := channel.NormalizePrimaryDerivedRoutes(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if channel.OtherSettings != original {
		t.Fatalf("settings without primary must stay untouched, got %s", channel.OtherSettings)
	}
}

func TestNormalizePrimaryDerivedRoutesToleratesBadJSON(t *testing.T) {
	channel := &Channel{OtherSettings: `{not json`}
	if err := channel.NormalizePrimaryDerivedRoutes(); err != nil {
		t.Fatalf("bad json must be left to validation, got %v", err)
	}
	if channel.OtherSettings != `{not json` {
		t.Fatal("bad json must not be rewritten")
	}
}

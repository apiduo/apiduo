package observability_setting

import "testing"

// normalize is the only gate between a hand-editable option row and the relay
// hot path, so every clamp/dedupe rule gets a case.
func TestNormalizeClampsAndCanonicalizes(t *testing.T) {
	in := ObservabilitySetting{
		MetadataSampleRate: 500,
		ContentSampleRate:  -20,
		Rules: []Rule{
			{Models: []string{" GPT-4O ", "gpt-4o", ""}, MetadataRate: 250, ContentRate: -5},
			{Days: []int{1, 1, 9}, Start: "9:5", End: "18:00", MetadataRate: 10, ContentRate: 10},
			{Start: "09:00", End: "", MetadataRate: 10, ContentRate: 10}, // half window
			{MetadataRate: 50, ContentRate: 50},                          // matches everything, no action
			{Models: []string{"img"}, Exclude: true},
		},
		TraceViewerUserIDs: []int{0, -3, 7, 7, 9},
	}

	out, compiled := normalize(in)

	if out.MetadataSampleRate != 100 {
		t.Fatalf("metadata rate must clamp to 100, got %d", out.MetadataSampleRate)
	}
	if out.ContentSampleRate != 0 {
		t.Fatalf("negative content rate must clamp to 0, got %d", out.ContentSampleRate)
	}
	if len(out.Rules) != 3 {
		t.Fatalf("expected 3 surviving rules (dedup + drop half window + drop catch-all), got %d: %+v", len(out.Rules), out.Rules)
	}
	if len(out.Rules[0].Models) != 1 || out.Rules[0].Models[0] != "gpt-4o" {
		t.Fatalf("models must be trimmed, lowercased and deduped: %v", out.Rules[0].Models)
	}
	if out.Rules[0].MetadataRate != 100 || out.Rules[0].ContentRate != 0 {
		t.Fatalf("rule rates must clamp: %+v", out.Rules[0])
	}
	if out.Rules[1].Start != "09:05" || out.Rules[1].End != "18:00" {
		t.Fatalf("clock must canonicalize: %q-%q", out.Rules[1].Start, out.Rules[1].End)
	}
	if len(out.Rules[1].Days) != 1 || out.Rules[1].Days[0] != 1 {
		t.Fatalf("weekdays must dedupe and drop out-of-range: %v", out.Rules[1].Days)
	}
	if len(out.TraceViewerUserIDs) != 2 || out.TraceViewerUserIDs[0] != 7 || out.TraceViewerUserIDs[1] != 9 {
		t.Fatalf("viewer ids: %v", out.TraceViewerUserIDs)
	}
	// Compiled rules carry the window flag the matcher needs; without it a
	// 09:00-18:00 rule would behave like "any time".
	if !compiled[1].HasWindow || compiled[1].StartMinute != 9*60+5 {
		t.Fatalf("compiled window: %+v", compiled[1])
	}
	if compiled[0].HasWindow {
		t.Fatal("a model-only rule must not be treated as a window")
	}
}

func TestNormalizeKeepsExclusionOrder(t *testing.T) {
	in := ObservabilitySetting{
		MetadataSampleRate: 100,
		ContentSampleRate:  100,
		Rules: []Rule{
			{Models: []string{"blocked"}, Exclude: true},
			{Models: []string{"blocked"}, MetadataRate: 5, ContentRate: 5},
		},
	}
	_, compiled := normalize(in)
	if len(compiled) != 2 {
		t.Fatalf("both rules must survive: %d", len(compiled))
	}
	if !compiled[0].Exclude || compiled[1].Exclude {
		t.Fatal("order must be preserved: exclusion first")
	}
}

func TestParseClockRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"25:00", "12:60", "noon", "12", "12:3:4"} {
		if minute, canonical := parseClock(bad); minute != -1 || canonical != "" {
			t.Fatalf("%q must be rejected, got (%d,%q)", bad, minute, canonical)
		}
	}
	if minute, canonical := parseClock("00:00"); minute != 0 || canonical != "00:00" {
		t.Fatalf("midnight must parse, got (%d,%q)", minute, canonical)
	}
}

// An unknown zone must fall back to local time rather than fail the load and
// leave the relay without a policy.
func TestResolveLocationFallsBack(t *testing.T) {
	if loc := resolveLocation("Not/AZone"); loc == nil {
		t.Fatal("invalid zone must fall back to local, not nil")
	}
	if loc := resolveLocation("Asia/Shanghai"); loc == nil || loc.String() != "Asia/Shanghai" {
		t.Fatalf("valid zone must load, got %v", loc)
	}
}

// A half-specified window has exactly one honest outcome: refuse the write.
// normalize() would drop both ends at read time, leaving the row claiming a
// window that never applies — the console then shows a setting that is not in
// force, which is the defect this check exists to prevent.
func TestValidateRuleWindowsRejectsHalfWindow(t *testing.T) {
	cases := []struct {
		name  string
		value string
		bad   bool
	}{
		{"both ends", `[{"models":["a"],"start":"09:00","end":"18:00"}]`, false},
		{"no window", `[{"models":["a"]}]`, false},
		{"start only", `[{"start":"09:00"}]`, true},
		{"end only", `[{"end":"18:00"}]`, true},
		{"bad start", `[{"start":"25:00","end":"18:00"}]`, true},
		{"bad end", `[{"start":"09:00","end":"noon"}]`, true},
		{"not json", `{"start":"09:00"}`, true},
		{"empty list", `[]`, false},
	}
	for _, tc := range cases {
		err := ValidateRuleWindows(RulesOptionKey, tc.value)
		if tc.bad && err == nil {
			t.Fatalf("%s: expected rejection, got nil", tc.name)
		}
		if !tc.bad && err != nil {
			t.Fatalf("%s: unexpected rejection: %s", tc.name, err.Error())
		}
	}
}

// The check must be keyed: other option rows pass through untouched.
func TestValidateRuleWindowsIgnoresOtherKeys(t *testing.T) {
	if err := ValidateRuleWindows("observability_setting.metadata_sample_rate", `{"start":"09:00"}`); err != nil {
		t.Fatalf("unrelated key must not be validated: %s", err.Error())
	}
}

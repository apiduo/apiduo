package observability_setting

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/pkg/apiduo/trace"
	"github.com/QuantumNous/new-api/setting/config"
)

// Rule is one sampling rule as the console edits it. Conditions are optional;
// the first matching rule wins, top to bottom.
//
// Times are "HH:MM" in the configured timezone. A window whose start is later
// than its end wraps midnight (22:00–06:00). Empty means "any".
type Rule struct {
	Models       []string `json:"models"`
	Days         []int    `json:"days"` // 0=Sunday … 6=Saturday
	Start        string   `json:"start"`
	End          string   `json:"end"`
	Exclude      bool     `json:"exclude"`
	MetadataRate int      `json:"metadata_rate"`
	ContentRate  int      `json:"content_rate"`
}

// ObservabilitySetting is the console-editable half of the trace
// configuration. Connection details (host, keys, on/off) stay in the
// environment: a bad settings row must never be able to redirect exports.
type ObservabilitySetting struct {
	// MetadataSampleRate: share of requests that produce a span at all. This
	// is also what makes the log page's trace link exist.
	MetadataSampleRate int `json:"metadata_sample_rate"`
	// ContentSampleRate: among spanned requests, the share that carries
	// prompt/answer. Lowering it keeps links working and cuts ingestion size.
	ContentSampleRate int `json:"content_sample_rate"`
	// Rules override the pair above by model and/or wall clock.
	Rules []Rule `json:"rules"`
	// ParentBasedSampling follows the W3C traceparent sampled bit so an
	// agent's tree stays coherent.
	ParentBasedSampling bool `json:"parent_based_sampling"`
	// Timezone is an IANA name ("Asia/Shanghai"). Empty means the server's
	// local zone, which container images often set to UTC — the console shows
	// the resolved zone so this is visible rather than guessed.
	Timezone string `json:"timezone"`
	// TraceViewerUserIDs lets specific non-root users see trace ids. Empty
	// means root only.
	TraceViewerUserIDs []int `json:"trace_viewer_user_ids"`
}

var observabilitySetting = ObservabilitySetting{
	// Seeded from the environment, not from a literal 100. Until the operator
	// saves this page there is no row in the options table, and "no row" must
	// mean "keep using LANGFUSE_SAMPLE_RATE" — freezing 100 here would turn the
	// first deploy of this version into an unannounced 10x ingestion increase
	// on every instance that had been sampling at 10%.
	MetadataSampleRate:  trace.EnvDefaultRates().Metadata,
	ContentSampleRate:   trace.EnvDefaultRates().Content,
	ParentBasedSampling: true,
}

func init() {
	config.GlobalConfig.Register("observability_setting", &observabilitySetting)
	// Intentionally no UpdateAndSync() here. The struct already equals the
	// environment config, so there is nothing to push; calling it would both
	// freeze those values as an explicit choice and make the hot-path gate
	// depend on package init order.
}

// Get returns a copy safe for readers.
func Get() ObservabilitySetting {
	out := observabilitySetting
	out.Rules = append([]Rule(nil), observabilitySetting.Rules...)
	for i := range out.Rules {
		out.Rules[i].Models = append([]string(nil), out.Rules[i].Models...)
		out.Rules[i].Days = append([]int(nil), out.Rules[i].Days...)
	}
	out.TraceViewerUserIDs = append([]int(nil), observabilitySetting.TraceViewerUserIDs...)
	return out
}

// UpdateAndSync normalizes the stored form and pushes it into the trace
// runtime. Called at startup (through updateOptionMap) and after every save,
// so a change takes effect without restarting the gateway.
func UpdateAndSync() {
	normalized, rules := normalize(observabilitySetting)
	observabilitySetting = normalized
	trace.ApplySettings(trace.Settings{
		Defaults:      trace.Rates{Metadata: normalized.MetadataSampleRate, Content: normalized.ContentSampleRate},
		Rules:         rules,
		Location:      resolveLocation(normalized.Timezone),
		ParentBased:   normalized.ParentBasedSampling,
		ViewerUserIDs: normalized.TraceViewerUserIDs,
	})
}

// normalize clamps, trims and canonicalizes so a hand-edited option row cannot
// smuggle in an out-of-range rate, a duplicate rule, or a malformed window.
func normalize(in ObservabilitySetting) (ObservabilitySetting, []trace.Rule) {
	out := ObservabilitySetting{
		MetadataSampleRate:  clampRate(in.MetadataSampleRate),
		ContentSampleRate:   clampRate(in.ContentSampleRate),
		ParentBasedSampling: in.ParentBasedSampling,
		Timezone:            strings.TrimSpace(in.Timezone),
	}
	compiled := make([]trace.Rule, 0, len(in.Rules))
	for _, rule := range in.Rules {
		models := normalizeModels(rule.Models)
		days := normalizeDays(rule.Days)
		startMinute, start := parseClock(rule.Start)
		endMinute, end := parseClock(rule.End)
		// A half-specified window is a configuration mistake: drop both ends
		// rather than silently sampling around the clock.
		if (start == "") != (end == "") {
			startMinute, endMinute = -1, -1
			start, end = "", ""
		}
		if len(models) == 0 && len(days) == 0 && startMinute < 0 && endMinute < 0 && !rule.Exclude {
			// A rule that matches everything with no action would shadow every
			// rule below it; that is what the defaults are for.
			continue
		}
		out.Rules = append(out.Rules, Rule{
			Models:       models,
			Days:         days,
			Start:        start,
			End:          end,
			Exclude:      rule.Exclude,
			MetadataRate: clampRate(rule.MetadataRate),
			ContentRate:  clampRate(rule.ContentRate),
		})
		compiled = append(compiled, trace.Rule{
			Models:      models,
			Days:        days,
			StartMinute: startMinute,
			EndMinute:   endMinute,
			HasWindow:   start != "" && end != "",
			Exclude:     rule.Exclude,
			Rates:       trace.Rates{Metadata: clampRate(rule.MetadataRate), Content: clampRate(rule.ContentRate)},
		})
	}
	seenUsers := make(map[int]struct{}, len(in.TraceViewerUserIDs))
	for _, id := range in.TraceViewerUserIDs {
		if id <= 0 {
			continue
		}
		if _, dup := seenUsers[id]; dup {
			continue
		}
		seenUsers[id] = struct{}{}
		out.TraceViewerUserIDs = append(out.TraceViewerUserIDs, id)
	}
	return out, compiled
}

func normalizeModels(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, model := range in {
		key := strings.ToLower(strings.TrimSpace(model))
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func normalizeDays(in []int) []int {
	seen := make(map[int]struct{}, len(in))
	out := make([]int, 0, len(in))
	for _, d := range in {
		if d < 0 || d > 6 {
			continue
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}

// parseClock turns "HH:MM" into minutes since midnight and the canonical form.
// Invalid input yields (-1, "") — "any time" — so a typo degrades to a wider
// window instead of a rule that never matches.
func parseClock(value string) (int, string) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return -1, ""
	}
	parts := strings.SplitN(trimmed, ":", 2)
	if len(parts) != 2 {
		return -1, ""
	}
	hour, errHour := strconv.Atoi(parts[0])
	minute, errMinute := strconv.Atoi(parts[1])
	if errHour != nil || errMinute != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return -1, ""
	}
	return hour*60 + minute, fmt.Sprintf("%02d:%02d", hour, minute)
}

func resolveLocation(name string) *time.Location {
	if name == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.Local
	}
	return loc
}

func clampRate(rate int) int {
	if rate < 0 {
		return 0
	}
	if rate > 100 {
		return 100
	}
	return rate
}

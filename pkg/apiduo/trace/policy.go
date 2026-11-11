package trace

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// Rates are percentages in [0,100].
type Rates struct {
	Metadata int
	Content  int
}

// Rule is one sampling rule. Every condition is optional: an empty Models list
// means "any model", an empty Days list "any day", and a negative minute bound
// "any time". Rules are evaluated top-down and the FIRST match wins, which is
// what makes the order meaningful (put "exclude this model" above "business
// hours").
//
// A window whose StartMinute is greater than its EndMinute wraps midnight, so
// 22:00–06:00 is expressible without splitting it into two rules.
type Rule struct {
	Models      []string `json:"models"`
	Days        []int    `json:"days"`         // 0=Sunday … 6=Saturday
	StartMinute int      `json:"start_minute"` // -1 = any
	EndMinute   int      `json:"end_minute"`   // -1 = any
	Exclude     bool     `json:"exclude"`
	Rates       Rates    `json:"rates"`
	// HasWindow distinguishes "no time condition" from a real 00:00–00:00
	// window. The zero value must mean "any time": every rule that only
	// filters on model or weekday leaves it false.
	HasWindow bool `json:"has_window"`

	models map[string]struct{}
	days   [7]bool
}

// Policy is the whole sampling decision: defaults plus ordered rules.
type Policy struct {
	Defaults Rates  `json:"defaults"`
	Rules    []Rule `json:"rules"`
}

// BodySettings is the capture window as the console edits it. Kept behind a
// pointer in Settings because nil must mean "leave the environment value
// alone": every caller that only manages sampling (tests, older bridge code)
// would otherwise zero BodyMode into BodyOff and silently stop capturing
// content on every save.
type BodySettings struct {
	Mode      BodyMode
	HeadBytes int
	TailBytes int
	// FullBytes is the full-mode per-payload cap. Zero means "leave the
	// environment value alone", exactly like the pointer itself: a console
	// payload saved before this field existed must not shrink a cap the
	// operator set in .env.
	FullBytes int
}

// QualitySettings is the per-export half of the console config: how much of
// the failed traffic is kept, whether it is scrubbed on the way out, and which
// environment tag the rows carry.
//
// Pointered in Settings for the same reason BodySettings is: both zero values
// here are meaningful and wrong as defaults (0% retry capture, redaction OFF),
// so nil has to mean "leave the environment value alone" rather than "set it
// to the Go zero value".
type QualitySettings struct {
	ErrorSampleRate int
	Redact          bool
	Environment     string
}

// EndpointSettings is where spans are POSTed and where the jump link points.
//
// Both used to be environment-only on the grounds that "a bad settings row
// must never redirect exports". That reasoning still holds — which is why the
// values are validated on write (http/https, no userinfo, no query) and why
// only root can save them — but a Langfuse migration should not require a
// redeploy, and the operator who owns the keys is the one person who may point
// them somewhere else.
//
// Host is the export target (often an internal container name); ConsoleURL is
// what a browser can reach. Empty ConsoleURL means "same as Host", exactly as
// it does in the environment.
type EndpointSettings struct {
	Host       string
	ConsoleURL string
}

// Settings is what the console writes; ApplySettings compiles it.
type Settings struct {
	Defaults      Rates
	Rules         []Rule
	Location      *time.Location
	ParentBased   bool
	ViewerUserIDs []int
	// Body is nil when the caller does not manage the capture window.
	Body *BodySettings
	// Quality is nil when the caller does not manage failure sampling,
	// redaction or the environment tag.
	Quality *QualitySettings
	// Endpoint is nil when the caller does not manage the export target.
	Endpoint *EndpointSettings
}

// compile lowers a rule into lookup structures so matching allocates nothing.
func (r *Rule) compile() {
	r.models = nil
	if len(r.Models) > 0 {
		r.models = make(map[string]struct{}, len(r.Models))
		for _, m := range r.Models {
			key := strings.ToLower(strings.TrimSpace(m))
			if key != "" {
				r.models[key] = struct{}{}
			}
		}
	}
	var zero [7]bool
	r.days = zero
	for _, d := range r.Days {
		if d >= 0 && d <= 6 {
			r.days[d] = true
		}
	}
}

func (r *Rule) matches(model string, now time.Time) bool {
	if r.models != nil {
		if _, ok := r.models[strings.ToLower(model)]; !ok {
			return false
		}
	}
	if r.days != [7]bool{} && !r.days[now.Weekday()] {
		return false
	}
	if r.HasWindow {
		minute := now.Hour()*60 + now.Minute()
		if r.StartMinute <= r.EndMinute {
			if minute < r.StartMinute || minute >= r.EndMinute {
				return false
			}
		} else { // wraps midnight
			if minute < r.StartMinute && minute >= r.EndMinute {
				return false
			}
		}
	}
	return true
}

// Resolve returns the rates for this request, or excluded=true when a rule
// says "never emit". A nil policy keeps everything sampled: losing traces is
// worse than a surprising default when a settings row is missing.
func (p *Policy) Resolve(model string, now time.Time) (Rates, bool) {
	if p == nil {
		return Rates{Metadata: 100, Content: 100}, false
	}
	for i := range p.Rules {
		rule := &p.Rules[i]
		if rule.matches(model, now) {
			if rule.Exclude {
				return Rates{}, true
			}
			return clampRates(rule.Rates), false
		}
	}
	return clampRates(p.Defaults), false
}

func clampRates(r Rates) Rates {
	return Rates{Metadata: clampRate(r.Metadata), Content: clampRate(r.Content)}
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

// sampleByTrace hashes the trace id (plus a salt per decision) so the verdict
// is a pure function of the trace: it survives restarts, needs no shared
// counter, and two gateway replicas make the same call without coordinating.
// A per-request RNG would do the opposite on all three counts.
func sampleByTrace(traceID, salt string, rate int) bool {
	if rate >= 100 {
		return true
	}
	if rate <= 0 || traceID == "" {
		return false
	}
	sum := sha256.Sum256([]byte("apiduo-sample\x00" + salt + "\x00" + traceID))
	// uint32 % 100 has negligible bias (2^32 is not a multiple of 100, but the
	// residue is 96/2^32), unlike byte % 100 which over-weights 0-55.
	return int(binary.BigEndian.Uint32(sum[:4])%100) < rate
}

// ApplySettings swaps the console-controlled knobs in atomically. Rates are
// clamped and rules recompiled, so a hand-edited option row cannot widen a
// range or leave stale lookups behind.
func ApplySettings(s Settings) {
	base := *current()
	rules := make([]Rule, 0, len(s.Rules))
	for _, rule := range s.Rules {
		compiled := Rule{
			Models:      append([]string(nil), rule.Models...),
			Days:        append([]int(nil), rule.Days...),
			StartMinute: rule.StartMinute,
			EndMinute:   rule.EndMinute,
			Exclude:     rule.Exclude,
			Rates:       clampRates(rule.Rates),
			HasWindow:   rule.HasWindow,
		}
		compiled.compile()
		rules = append(rules, compiled)
	}
	base.Policy = &Policy{Defaults: clampRates(s.Defaults), Rules: rules}
	if s.Location != nil {
		base.Location = s.Location
	} else {
		base.Location = time.Local
	}
	base.ParentBased = s.ParentBased
	base.TraceViewerUserIDs = append([]int(nil), s.ViewerUserIDs...)
	if s.Body != nil {
		base.BodyMode = s.Body.Mode
		base.HeadBytes, base.TailBytes = ClampBodyWindow(s.Body.HeadBytes, s.Body.TailBytes)
		if s.Body.FullBytes > 0 {
			base.FullMaxBytes = ClampBodyFullBytes(s.Body.FullBytes)
		}
	}
	if s.Quality != nil {
		base.ErrorSampleRate = clampRate(s.Quality.ErrorSampleRate)
		base.Redact = s.Quality.Redact
		base.Environment = strings.TrimSpace(s.Quality.Environment)
	}
	if s.Endpoint != nil {
		host := strings.TrimRight(strings.TrimSpace(s.Endpoint.Host), "/")
		console := strings.TrimRight(strings.TrimSpace(s.Endpoint.ConsoleURL), "/")
		// A change of export target cannot be applied to a worker that already
		// captured the old one: its route is fixed for the lifetime of the
		// goroutine, on purpose (a stale worker must never be re-aimed
		// mid-flight, or a batch queued for the canary lands in production).
		// Dropping the exporter makes the next enqueue build a worker aimed at
		// the new endpoint; the old goroutine drains what it already holds and
		// blocks forever on a channel nobody feeds — one idle goroutine per
		// endpoint change, which is a fair price for not restarting the gateway.
		if host != base.Host {
			resetExporter(&base)
		}
		base.Host = host
		base.ConsoleURL = console
	}
	currentConf.Store(&base)
	enabledFast.Store(base.Enabled)
}

// ClampBodyWindow bounds the console-editable window. Write-time validation
// already refuses head+tail above MaxBodyWindowBytes; this is the runtime
// backstop for a hand-edited option row, and it keeps the ratio the operator
// chose instead of zeroing one end.
//
// Exported so the settings module normalizes with the same arithmetic rather
// than growing a second copy that drifts.
func ClampBodyWindow(head, tail int) (int, int) {
	head = max(head, 0)
	tail = max(tail, 0)
	if total := head + tail; total > MaxBodyWindowBytes {
		head = head * MaxBodyWindowBytes / total
		tail = MaxBodyWindowBytes - head
	}
	return head, tail
}

// TraceViewerAllowed reports whether a non-root user is on the viewer list.
// Root is decided by the caller (this package imports no role constants); an
// empty list means nobody but root.
func TraceViewerAllowed(userID int) bool {
	if userID <= 0 {
		return false
	}
	for _, id := range current().TraceViewerUserIDs {
		if id == userID {
			return true
		}
	}
	return false
}

// ServerZone exposes the timezone the policy is evaluated in, plus the current
// policy clock, so the console can show "windows are read in Asia/Shanghai,
// now 03:20" instead of leaving operators to guess the container's TZ.
//
// The offset is part of the answer. An unset TZ resolves to time.Local, whose
// String() is the bare word "Local" — a value that cannot tell an operator
// whether a 09:00 rule is being read as UTC or as UTC+8, which is exactly the
// mistake this readout exists to prevent.
func ServerZone() (string, time.Time) {
	loc := current().Location
	if loc == nil {
		loc = time.Local
	}
	now := time.Now().In(loc)
	_, offset := now.Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	return fmt.Sprintf("%s (UTC%s%02d:%02d)", loc.String(), sign, offset/3600, offset%3600/60), now
}

// Package trace is apiduo's OTLP/Langfuse observability exporter.
//
// It follows the same dependency rule as pkg/apiduo/discount: zero imports of
// this repository's own packages (model/service/common/relay). Everything it
// needs from the request path arrives as plain types or *gin.Context, and
// type translation happens in bridge files next to the upstream call sites.
//
// Performance contract (see docs/apiduo/langfuse-trace.md §3.5):
//   - every exported entry point first checks one atomic.Bool; disabled means
//     "no allocation, no lock, no work"
//   - the hot path never serializes, never runs regexes, never unmarshals;
//     frame parsing and redaction happen in the flush worker
//   - queues are bounded and full means drop; tracing must never push back on
//     the relay (log-writer may fall back to synchronous inserts because logs
//     are accounting records — traces are not, and must not copy that policy)
package trace

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// BodyMode selects how much request/response content is captured.
type BodyMode int8

const (
	// BodyOff captures metadata only.
	BodyOff BodyMode = iota
	// BodyHeadTail keeps the first HeadBytes and the last TailBytes of each
	// payload and records how much was dropped. This is the default: SSE
	// streams carry role/model/first delta up front and finish_reason/usage
	// at the end, so both ends carry almost all the signal.
	BodyHeadTail
	// BodyFull keeps payloads up to fullMaxBytes. Costs ~512MB per thousand
	// concurrent requests (see the memory budget table) — opt-in only.
	BodyFull
)

// Config is the immutable runtime configuration. It is loaded from the
// environment once and can only be replaced wholesale (tests do that via
// setConfig), so readers never need locks.
type Config struct {
	Enabled    bool
	Host       string // Langfuse base URL (OTLP export endpoint), no trailing slash
	ConsoleURL string // browser-facing base for the jump link; "" = same as Host
	PublicKey  string // Basic auth user (pk-lf-...)
	SecretKey  string // Basic auth password, never leaves this process
	ProjectID  string // for the console jump link
	// Policy decides sampling from the request's model and the wall clock.
	// One ordered rule list covers "per model", "exclude these" and "business
	// hours only" so the console cannot drift into three conflicting sources.
	Policy *Policy
	// Location is the timezone the policy's day/minute windows are read in.
	Location *time.Location
	// ParentBased follows the W3C traceparent sampled bit: an upstream agent
	// that already decided "sample" keeps this span, and one that said "do
	// not sample" is respected. Off means every request is drawn locally.
	ParentBased        bool
	TraceViewerUserIDs []int
	// ErrorSampleRate promotes failed requests that local sampling dropped
	// (default 100): failures are the rows an operator opens Langfuse for.
	ErrorSampleRate int
	BodyMode        BodyMode
	HeadBytes       int
	TailBytes       int
	TimeoutSeconds  int
	QueueMeta       int
	QueueContent    int
	FlushIntervalMS int  // batch window; tests shorten it
	Redact          bool // scrub API keys / phones / emails before export
}

const (
	defaultHeadBytes      = 32 * 1024
	defaultTailBytes      = 16 * 1024
	fullMaxBytes          = 2 * 1024 * 1024
	defaultQueueMeta      = 2048
	defaultQueueContent   = 512
	defaultTimeoutSeconds = 3
	defaultSampleRate     = 100
	defaultErrorRate      = 100
)

func envInt(key string, def int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func envBool(key string) bool {
	switch os.Getenv(key) {
	case "1", "true", "TRUE", "True", "yes":
		return true
	}
	return false
}

func envBodyMode(key string) BodyMode {
	switch os.Getenv(key) {
	case "off":
		return BodyOff
	case "full":
		return BodyFull
	default:
		return BodyHeadTail
	}
}

// EnvDefaultRates is the sampling pair the environment asks for.
//
// The settings module seeds its console form with this instead of a literal
// 100. A console showing "100%" while the exporter runs at the .env rate is
// not a cosmetic mismatch: every "why is this request missing from Langfuse"
// investigation would start from a false premise. Keeping the read in one
// place also means loadFromEnv cannot drift from what the form displays.
func EnvDefaultRates() Rates {
	return Rates{
		Metadata: clampRate(envInt("LANGFUSE_SAMPLE_RATE", defaultSampleRate)),
		Content:  clampRate(envInt("LANGFUSE_CONTENT_SAMPLE_RATE", defaultSampleRate)),
	}
}

func loadFromEnv() *Config {
	return &Config{
		Enabled:    envBool("LANGFUSE_ENABLED"),
		Host:       strings.TrimRight(os.Getenv("LANGFUSE_HOST"), "/"),
		ConsoleURL: strings.TrimRight(os.Getenv("LANGFUSE_CONSOLE_URL"), "/"),
		PublicKey:  os.Getenv("LANGFUSE_PUBLIC_KEY"),
		SecretKey:  os.Getenv("LANGFUSE_SECRET_KEY"),
		ProjectID:  os.Getenv("LANGFUSE_PROJECT_ID"),
		Policy:     &Policy{Defaults: EnvDefaultRates()},
		Location:        time.Local,
		ParentBased:     true,
		ErrorSampleRate: clampRate(envInt("LANGFUSE_ERROR_SAMPLE_RATE", defaultErrorRate)),
		BodyMode:        envBodyMode("LANGFUSE_BODY_MODE"),
		HeadBytes:       envInt("LANGFUSE_BODY_HEAD_BYTES", defaultHeadBytes),
		TailBytes:       envInt("LANGFUSE_BODY_TAIL_BYTES", defaultTailBytes),
		TimeoutSeconds:  envInt("LANGFUSE_TIMEOUT_SECONDS", defaultTimeoutSeconds),
		QueueMeta:       envInt("LANGFUSE_QUEUE_META", defaultQueueMeta),
		QueueContent:    envInt("LANGFUSE_QUEUE_CONTENT", defaultQueueContent),
		FlushIntervalMS: envInt("LANGFUSE_FLUSH_INTERVAL_MS", 5000),
		Redact:          os.Getenv("LANGFUSE_REDACT") != "false", // default on
	}
}

var (
	configOnce   sync.Once
	currentConf  atomic.Pointer[Config]
	enabledFast  atomic.Bool
	shutdownOnce sync.Once
)

// init arms the hot-path gate before the first request can arrive.
//
// This is not an optimization: Enabled() is a bare atomic load on purpose
// (performance contract §3.5), so if nothing has loaded the environment yet
// it answers false and Begin() returns before it ever calls current(). The
// gate would then stay shut for the life of the process — until some other
// caller (the admin config endpoint, an exporter flush) happened to load the
// config, making tracing switch on mid-flight. A local e2e run showed exactly
// that: zero spans until the config page was visited.
//
// Freezing configOnce here also means current() never re-reads the
// environment; tests override it wholesale via setConfig.
func init() {
	armFromEnv()
}

// armFromEnv installs the environment config and opens the fast gate. Split
// out from init so the arming behaviour is directly testable.
func armFromEnv() {
	c := loadFromEnv()
	currentConf.Store(c)
	enabledFast.Store(c.Enabled)
	configOnce.Do(func() {})
}

// current returns the active config, loading it from the environment on
// first use so tests can install their own before the first call.
func current() *Config {
	configOnce.Do(func() {
		if currentConf.Load() == nil {
			currentConf.Store(loadFromEnv())
		}
		enabledFast.Store(currentConf.Load().Enabled)
	})
	return currentConf.Load()
}

// setConfig installs a config for tests. It also (re)binds the fast gate and
// rebuilds the queues so worker tests start from a clean slate.
func setConfig(c *Config) {
	configOnce.Do(func() {})
	currentConf.Store(c)
	enabledFast.Store(c.Enabled)
	resetExporter(c)
}

// Enabled is the hot-path gate. One atomic load; safe to call from anywhere.
func Enabled() bool {
	return enabledFast.Load()
}

// CurrentConfig returns a copy of the active config. Values only — callers
// cannot mutate live state through it.
func CurrentConfig() Config {
	c := current()
	if c == nil {
		return Config{}
	}
	return *c
}

// ConsoleBase is the base URL a browser can reach. The export path may be an
// internal container name (apiduo-langfuse-web:3000) that resolves only
// inside the docker network; the jump link must use the public console URL
// instead, or every click lands on an unresolvable host.
func (c Config) ConsoleBase() string {
	if c.ConsoleURL != "" {
		return c.ConsoleURL
	}
	return c.Host
}

// BodyModeName renders the capture mode for the admin config endpoint.
func (c Config) BodyModeName() string {
	switch c.BodyMode {
	case BodyOff:
		return "off"
	case BodyFull:
		return "full"
	default:
		return "headtail"
	}
}

package trace

import (
	"regexp"
	"strings"
)

// Redaction runs in the flush worker, after truncation and parsing, on the
// text that is about to leave the process. The request path never runs a
// regex (docs §3.4).
//
// Extension point: append a rule here; the table is the whole configuration.
// Rules are applied in order, so specific patterns (API keys) come before
// generic ones (long digit runs).

type redactRule struct {
	pattern     *regexp.Regexp
	replacement string
}

var redactRules = []redactRule{
	// Vendor API keys. The sk- prefix is shared by OpenAI/Anthropic and the
	// new-api gateway itself; 16+ body chars keeps "sk-short" prose intact.
	{regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}`), "[REDACTED:api_key]"},
	{regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{16,}`), "Bearer [REDACTED:token]"},
	// Chinese mainland ID card (18 digits with X tail allowed).
	{regexp.MustCompile(`\b\d{17}[\dXx]\b`), "[REDACTED:id_card]"},
	// Chinese mainland mobile numbers.
	{regexp.MustCompile(`\b1[3-9]\d{9}\b`), "[REDACTED:phone]"},
	// Bank cards: 16-19 digits in groups.
	{regexp.MustCompile(`\b(?:\d{4}[ -]?){3,4}\d{1,4}\b`), "[REDACTED:bank_card]"},
	{regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), "[REDACTED:email]"},
}

// Redact replaces sensitive substrings in text. A no-op for empty input.
func Redact(text string) string {
	if text == "" {
		return ""
	}
	for _, rule := range redactRules {
		text = rule.pattern.ReplaceAllString(text, rule.replacement)
	}
	return text
}

// redactForExport applies the switch: some deployments capture content for
// audit specifically to keep raw identifiers.
func redactForExport(conf *Config, text string) string {
	if !conf.Redact {
		return text
	}
	return Redact(text)
}

// needsRedaction reports whether text has anything the rules would touch —
// used by tests and, later, by sampling decisions that prefer dirty samples.
func needsRedaction(text string) bool {
	return strings.Contains(Redact(text), "[REDACTED:")
}

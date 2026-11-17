package urlguard

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// APIDUO:result-url:BEGIN
//
// What an upstream-supplied result address has to satisfy.
//
// Two different questions share this value and they must not be answered by the
// same rule:
//
//  1. The gateway stores it and hands it to a client. The client may render it
//     as a link, so a scheme it would execute (`javascript:`, `data:`) turns the
//     gateway into a delivery channel for someone else's payload. What matters
//     here is the shape: absolute, http(s), a host, no embedded credentials.
//  2. The gateway may *fetch* it — artifact storage copies the object. That is a
//     request forgery primitive, so the fetch path applies its own validator
//     (validateArtifactFetchURL) which resolves the host and refuses loopback and
//     private addresses.
//
// Applying (2) here would be wrong: a result URL pointing at an internal MinIO
// is legitimate to hand out, and refusing it would break those deployments
// without making anything safer. Applying only (1) there would be a hole. Each
// path keeps the rule it needs.

// TaskResultURLMaxLength bounds the address the gateway stores.
//
// Signed CDN links run to a couple of kilobytes; the ceiling exists so a
// misconfigured extraction path cannot put an arbitrarily large string into the
// task row and then into every poll response.
const TaskResultURLMaxLength = 8192

// ValidateTaskResultURL accepts only an absolute http(s) address.
func ValidateTaskResultURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("result url is empty")
	}
	if len(trimmed) > TaskResultURLMaxLength {
		return "", fmt.Errorf("result url exceeds %d bytes", TaskResultURLMaxLength)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("result url is not a valid url: %w", err)
	}
	if !parsed.IsAbs() {
		return "", errors.New("result url is not absolute")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("result url scheme %q is not http(s)", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", errors.New("result url has no host")
	}
	if parsed.User != nil {
		return "", errors.New("result url carries credentials")
	}
	return trimmed, nil
}

// APIDUO:result-url:END

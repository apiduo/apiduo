package model

import (
	"errors"
	"net"
	"strings"
)

// APIDUO:token-ip-allowlist:BEGIN
//
// An access token may be pinned to the networks it is used from. This is the
// cheapest defence against the most common leak: a key copied into a chat
// message, a CI variable, or someone's laptop. The key still works, but only
// from where it is supposed to.
//
// It is deliberately a separate file so the upstream struct only gains one
// field, which keeps rebases small.
//
// Semantics match the inference-API tokens (see Token.GetIpLimits): entries are
// separated by newlines or commas, each entry is a CIDR block or a single
// address, and an empty list means "no restriction".

const (
	// AccessTokenMaxIpEntries bounds how many networks one token may carry.
	//
	// The allowlist is parsed on every request that uses the token, and the cost
	// grows linearly with the entry count: 10 entries cost about 2µs, 1000 cost
	// about 160µs, 20000 cost about 3ms. Those numbers sit in the authentication
	// path, so an oversized list would tax every call including 404s. A real
	// deployment needs a handful of entries.
	AccessTokenMaxIpEntries = 128

	// AccessTokenMaxIpBytes bounds the raw string for the same reason, and also
	// keeps one row from bloating the table. 128 entries never reach this.
	AccessTokenMaxIpBytes = 4096
)

// ErrAccessTokenIpDenied is returned when a restricted token is used from a
// network that is not on its list.
var ErrAccessTokenIpDenied = errors.New("access token is not allowed from this address")

// GetIpLimits returns the parsed allowlist. A malformed list yields no entries,
// which the caller must treat as "unrestricted" rather than "deny everything" —
// see HasIpRestriction for why that distinction matters.
func (token *UserAccessToken) GetIpLimits() []string {
	if token == nil || token.AllowIps == nil {
		return nil
	}
	// Split on every separator at once. Stripping commas without splitting on
	// them would glue `10.0.0.0/8, 192.168.1.0/24` into
	// `10.0.0.0/8192.168.1.0/24`, which parses as neither address nor CIDR and
	// therefore silently denies everything.
	entries := strings.FieldsFunc(*token.AllowIps, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ' ' || r == '\t'
	})
	if len(entries) == 0 {
		return nil
	}
	return entries
}

// HasIpRestriction reports whether the operator asked for a restriction at all.
//
// It looks at the raw value instead of the parsed list on purpose: a typo in the
// allowlist must not silently turn into "no restriction". An unparseable entry
// still counts as a restriction, so the request is denied rather than allowed
// from anywhere.
func (token *UserAccessToken) HasIpRestriction() bool {
	if token == nil || token.AllowIps == nil {
		return false
	}
	return strings.TrimSpace(*token.AllowIps) != ""
}

// APIDUO:token-ip-allowlist:END

// ValidateIpAllowlist rejects a list that would never match anything.
//
// Without this check a typo such as `10.0.0.0/33` is stored happily and then
// denies every request, which looks exactly like a broken deployment. Failing at
// save time turns a silent outage into a form error.
func ValidateIpAllowlist(raw string) error {
	if len(raw) > AccessTokenMaxIpBytes {
		return ErrAccessTokenIpTooLarge
	}
	entries := (&UserAccessToken{AllowIps: &raw}).GetIpLimits()
	if len(entries) > AccessTokenMaxIpEntries {
		return ErrAccessTokenIpTooLarge
	}

	for _, entry := range entries {
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			if net.ParseIP(entry) == nil {
				return ErrAccessTokenIpInvalid
			}
			continue
		}
		// A zero-length prefix covers only one address family: `0.0.0.0/0` is
		// every IPv4 address but no IPv6 one, and `::/0` the reverse. Accepting
		// it invites two mistakes — believing it means "allow everything" and
		// accidentally locking out clients on the other family. "No restriction"
		// has exactly one spelling: leave the field empty.
		if ones, _ := network.Mask.Size(); ones == 0 {
			return ErrAccessTokenIpTooBroad
		}
	}
	return nil
}

var (
	// ErrAccessTokenIpInvalid marks an allowlist entry that is not an address or
	// a CIDR block.
	ErrAccessTokenIpInvalid = errors.New("access token IP allowlist contains an invalid address")
	// ErrAccessTokenIpTooLarge marks a list that would make every request pay
	// for parsing it.
	ErrAccessTokenIpTooLarge = errors.New("access token IP allowlist has too many entries")
	// ErrAccessTokenIpTooBroad marks a zero-length prefix.
	ErrAccessTokenIpTooBroad = errors.New("access token IP allowlist uses an all-addresses prefix")
)

// SetAccessTokenIpAllowlist replaces the restriction on one token. An empty
// value clears it, which is why the column is written from a map: GORM skips
// nil fields in a struct update, so clearing would silently do nothing.
func SetAccessTokenIpAllowlist(userID, id int, raw string) error {
	trimmed := strings.TrimSpace(raw)
	var value any
	if trimmed != "" {
		value = trimmed
	}
	return DB.Model(&UserAccessToken{}).
		Where("id = ? AND user_id = ?", id, userID).
		Updates(map[string]any{"allow_ips": value}).Error
}

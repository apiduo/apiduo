package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAccessTokenIpLimitsParsing(t *testing.T) {
	makeToken := func(raw string) *UserAccessToken {
		value := raw
		return &UserAccessToken{AllowIps: &value}
	}

	// No value at all means "unrestricted", which is the behaviour every
	// existing token relies on.
	assert.False(t, (&UserAccessToken{}).HasIpRestriction())
	assert.Empty(t, (&UserAccessToken{}).GetIpLimits())

	assert.False(t, makeToken("   ").HasIpRestriction())
	assert.Empty(t, makeToken("   ").GetIpLimits())

	// Mixed separators are accepted because operators paste lists from
	// spreadsheets and dashboards.
	parsed := makeToken("10.0.0.0/8, 192.168.1.0/24\n203.0.113.7")
	assert.True(t, parsed.HasIpRestriction())
	assert.Equal(t, []string{"10.0.0.0/8", "192.168.1.0/24", "203.0.113.7"}, parsed.GetIpLimits())
}

func TestValidateIpAllowlist(t *testing.T) {
	require.NoError(t, ValidateIpAllowlist(""))
	require.NoError(t, ValidateIpAllowlist("10.0.0.0/8"))
	require.NoError(t, ValidateIpAllowlist("203.0.113.7"))
	require.NoError(t, ValidateIpAllowlist("::1"))
	require.NoError(t, ValidateIpAllowlist("2001:db8::/32"))
	require.NoError(t, ValidateIpAllowlist("10.0.0.0/8\n192.168.0.1"))

	// A typo must fail at save time. Storing it would silently deny every
	// request later, which is indistinguishable from a broken deployment.
	assert.ErrorIs(t, ValidateIpAllowlist("10.0.0.0/33"), ErrAccessTokenIpInvalid)
	assert.ErrorIs(t, ValidateIpAllowlist("not-an-ip"), ErrAccessTokenIpInvalid)
	assert.ErrorIs(t, ValidateIpAllowlist("10.0.0.0/8\n999.1.1.1"), ErrAccessTokenIpInvalid)
}

func TestHasIpRestrictionSurvivesUnparseableValues(t *testing.T) {
	// The distinction that matters: "asked for a restriction" is judged on the
	// raw value, so a malformed entry denies requests instead of opening the
	// token to the world.
	value := "definitely not an address"
	token := &UserAccessToken{AllowIps: &value}
	assert.True(t, token.HasIpRestriction())
	// Parsing does not silently drop junk: every word comes back as an entry and
	// validation is what rejects them, so the failure is visible at save time.
	assert.NotEmpty(t, token.GetIpLimits())
	assert.ErrorIs(t, ValidateIpAllowlist(value), ErrAccessTokenIpInvalid)
}

func TestValidateIpAllowlistRejectsOversizedLists(t *testing.T) {
	// The allowlist is parsed on every request, so its size is a hot-path cost.
	// Both axes are bounded: too many entries, or one oversized blob.
	entries := make([]string, AccessTokenMaxIpEntries+1)
	for i := range entries {
		entries[i] = "10.0.0.0/8"
	}
	assert.ErrorIs(t, ValidateIpAllowlist(strings.Join(entries, "\n")), ErrAccessTokenIpTooLarge)

	// Exactly at the entry cap is fine.
	entries = entries[:AccessTokenMaxIpEntries]
	require.NoError(t, ValidateIpAllowlist(strings.Join(entries, "\n")))

	// A single enormous token is rejected by the byte cap even though it is one
	// entry.
	huge := "10.0.0.0/8" + strings.Repeat(" ", AccessTokenMaxIpBytes)
	assert.ErrorIs(t, ValidateIpAllowlist(huge), ErrAccessTokenIpTooLarge)
}

func TestValidateIpAllowlistRejectsAllAddressesPrefix(t *testing.T) {
	// These look like "allow everything" but each covers one family only, so a
	// dual-stack client on the other family would be locked out. The single
	// spelling for "no restriction" is an empty field.
	for _, raw := range []string{"0.0.0.0/0", "::/0", "0.0.0.0/0\n10.0.0.0/8"} {
		assert.ErrorIs(t, ValidateIpAllowlist(raw), ErrAccessTokenIpTooBroad, raw)
	}

	// Anything narrower is a real restriction and stays allowed.
	for _, raw := range []string{"0.0.0.0/1", "10.0.0.0/8", "::/1", "128.0.0.0/1"} {
		assert.NoError(t, ValidateIpAllowlist(raw), raw)
	}
}

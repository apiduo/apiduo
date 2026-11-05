package model

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub, priv
}

func publicKeyPEM(t *testing.T, pub ed25519.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func TestDecodeAgentPublicKeyAcceptsBothShapes(t *testing.T) {
	pub, _ := newTestKeyPair(t)

	fromBase64, err := DecodeAgentPublicKey(base64.StdEncoding.EncodeToString(pub))
	require.NoError(t, err)
	assert.Equal(t, pub, fromBase64)

	fromPEM, err := DecodeAgentPublicKey(publicKeyPEM(t, pub))
	require.NoError(t, err)
	assert.Equal(t, pub, fromPEM)

	// A paste that lost its trailing padding is common enough to tolerate.
	raw := base64.RawStdEncoding.EncodeToString(pub)
	fromRaw, err := DecodeAgentPublicKey(raw)
	require.NoError(t, err)
	assert.Equal(t, pub, fromRaw)
}

func TestDecodeAgentPublicKeyRejectsGarbage(t *testing.T) {
	// Each of these would otherwise produce a key that silently never verifies,
	// leaving the operator to debug a "signature invalid" error instead.
	for _, raw := range []string{
		"",
		"   ",
		"not-a-key",
		"AAAA", // valid base64, wrong length
		"-----BEGIN PUBLIC KEY-----\nbroken\n-----END PUBLIC KEY-----",
		base64.StdEncoding.EncodeToString([]byte("too short")),
	} {
		_, err := DecodeAgentPublicKey(raw)
		assert.ErrorIs(t, err, ErrAgentPublicKey, raw)
	}
}

func TestVerifyAgentSignature(t *testing.T) {
	pub, priv := newTestKeyPair(t)
	encoded := base64.StdEncoding.EncodeToString(pub)
	challenge := "3q2-7wYtK9nJm4pR8sVb1cXd6eZf0gHi"

	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(challenge)))
	require.NoError(t, VerifyAgentSignature(encoded, challenge, signature))

	// A different challenge must not verify: that is what stops a captured
	// signature from being replayed against a fresh nonce.
	assert.ErrorIs(t, VerifyAgentSignature(encoded, challenge+"x", signature), ErrAgentSignature)

	// A different key pair verifies against itself, which proves the rejection
	// below is caused by the key mismatch and not by a broken signature.
	otherPub, otherPriv := newTestKeyPair(t)
	otherSig := base64.StdEncoding.EncodeToString(ed25519.Sign(otherPriv, []byte(challenge)))
	require.NoError(t, VerifyAgentSignature(base64.StdEncoding.EncodeToString(otherPub), challenge, otherSig))

	// Crossed over, it must not: this is the "another private key tries to
	// impersonate the registered one" case.
	assert.ErrorIs(t, VerifyAgentSignature(encoded, challenge, otherSig), ErrAgentSignature)

	// Truncated and malformed signatures are rejected rather than panicking.
	assert.ErrorIs(t, VerifyAgentSignature(encoded, challenge, ""), ErrAgentSignature)
	assert.ErrorIs(t, VerifyAgentSignature(encoded, challenge, "AAAA"), ErrAgentSignature)
	assert.ErrorIs(t, VerifyAgentSignature(encoded, challenge, "not-base64!!"), ErrAgentSignature)
}

func TestAgentChallengeIsSingleUse(t *testing.T) {
	challenge, expiresAt, err := IssueAgentChallenge(7)
	require.NoError(t, err)
	assert.NotEmpty(t, challenge)
	assert.Greater(t, expiresAt, int64(0))

	require.NoError(t, ConsumeAgentChallenge(challenge, 7))

	// Replaying the same nonce is the whole attack this defends against.
	assert.ErrorIs(t, ConsumeAgentChallenge(challenge, 7), ErrAgentChallengeUsed)
}

func TestAgentChallengeIsBoundToItsKey(t *testing.T) {
	challenge, _, err := IssueAgentChallenge(11)
	require.NoError(t, err)

	// A nonce issued for one key must not be redeemable by another, or a
	// signature over someone else's challenge would be useful.
	assert.ErrorIs(t, ConsumeAgentChallenge(challenge, 12), ErrAgentChallengeUsed)

	// The rejected attempt still burned it, so the legitimate owner now has to
	// ask for a new one. Failing closed is the intended behaviour.
	assert.ErrorIs(t, ConsumeAgentChallenge(challenge, 11), ErrAgentChallengeUsed)
}

func TestConsumeAgentChallengeRejectsUnknownValue(t *testing.T) {
	assert.ErrorIs(t, ConsumeAgentChallenge("never-issued", 1), ErrAgentChallengeUsed)
	assert.ErrorIs(t, ConsumeAgentChallenge("", 1), ErrAgentChallengeUsed)
}

func TestAgentKeyScopesRoundTrip(t *testing.T) {
	var key AgentKey

	// A key with no grant must not parse into something usable.
	assert.Empty(t, key.GetScopes())

	require.NoError(t, key.SetScopes([]string{"nav_app:read", "nav_app:write"}))
	assert.Equal(t, []string{"nav_app:read", "nav_app:write"}, key.GetScopes())

	// Corrupt storage grants nothing rather than everything.
	key.Scopes = "{not json"
	assert.Empty(t, key.GetScopes())
}

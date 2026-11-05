package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// Anonymous key-protected endpoints: an agent proves possession of a private
// key, trades the proof for a short-lived access token, and then uses that token
// exactly like a personal access token.
//
// The signature is not checked on every request on purpose. Reusing the token
// middleware keeps the hot path unchanged, keeps audit attribution (token_ref)
// intact, and means an agent tool that only understands `Authorization: Bearer`
// still works — it just has to call /api/agent/exchange first.

type agentKeyCreateRequest struct {
	Name      string   `json:"name"`
	PublicKey string   `json:"public_key"`
	Scopes    []string `json:"scopes"`
	ExpiresAt int64    `json:"expires_at"`
}

type agentChallengeRequest struct {
	KeyID int `json:"key_id"`
}

type agentExchangeRequest struct {
	KeyID     int    `json:"key_id"`
	Challenge string `json:"challenge"`
	Signature string `json:"signature"`
	// Name lets one key serve several jobs while keeping them apart in the audit
	// trail. Falls back to the key name.
	Name string `json:"name"`
}

// publicKeyFingerprint identifies a key without exposing it in logs.
func publicKeyFingerprint(raw string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	return hex.EncodeToString(sum[:8])
}

// CreateAgentKey registers a public key. A human must be present: this is the
// same class of action as issuing a personal access token, so it carries the
// same step-up requirement.
func CreateAgentKey(c *gin.Context) {
	identity, ok := requireBrowserSession(c)
	if !ok {
		return
	}

	var req agentKeyCreateRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		writeAccessTokenError(c, http.StatusBadRequest, "AGENT_KEY_INVALID_REQUEST", "Invalid request body.")
		return
	}
	name, ok := normalizeAccessTokenName(req.Name)
	if !ok {
		writeAccessTokenError(c, http.StatusBadRequest, "AGENT_KEY_NAME_INVALID", "Key name must be 1 to 64 characters.")
		return
	}

	// Validate the key material before consuming the user's verification, so a
	// typo does not burn a proof.
	if _, err := model.DecodeAgentPublicKey(req.PublicKey); err != nil {
		writeAccessTokenError(c, http.StatusBadRequest, "AGENT_KEY_PUBLIC_KEY_INVALID", "The public key is not a valid Ed25519 key. Paste a base64 key or a PEM public key block.")
		return
	}

	now := common.GetTimestamp()
	if req.ExpiresAt != 0 && req.ExpiresAt < now+accessTokenMinLifetimeSecs {
		writeAccessTokenError(c, http.StatusBadRequest, "AGENT_KEY_EXPIRY_INVALID", "Key expiry must be at least one hour from now.")
		return
	}

	scopes, err := service.NormalizeAccessTokenScopes(identity.UserID, c.GetInt("role"), req.Scopes)
	switch {
	case errors.Is(err, service.ErrAccessTokenScopeForbidden):
		writeAccessTokenError(c, http.StatusBadRequest, "AGENT_KEY_SCOPE_FORBIDDEN", "You cannot grant one or more of these permissions.")
		return
	case err != nil:
		writeAccessTokenError(c, http.StatusBadRequest, "AGENT_KEY_SCOPE_INVALID", "Select at least one valid permission.")
		return
	}

	proofContext, err := common.Marshal(service.AgentKeyRegisterContext{
		Scopes:      scopes,
		ExpiresAt:   req.ExpiresAt,
		PublicKeyID: publicKeyFingerprint(req.PublicKey),
	})
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	if middleware.RequireSecurityProof(c, service.VerificationOperation{
		Scope:   service.VerificationScopeAgentKeyRegister,
		Context: proofContext,
	}) == nil {
		return
	}

	key := &model.AgentKey{
		Name:        name,
		PublicKey:   strings.TrimSpace(req.PublicKey),
		KeyType:     "ed25519",
		ExpiresAt:   req.ExpiresAt,
		CreatedAt:   now,
		AuthVersion: identity.UserAuthVersion,
	}
	if err := key.SetScopes(scopes); err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	if err := model.CreateAgentKey(identity.UserID, key, model.AgentKeyMaxPerUser); err != nil {
		if errors.Is(err, model.ErrAccessTokenLimit) {
			writeAccessTokenError(c, http.StatusBadRequest, "AGENT_KEY_LIMIT", "You can register at most {{count}} agent keys", map[string]any{"count": model.AgentKeyMaxPerUser})
			return
		}
		writeSecurityOperationError(c, err)
		return
	}

	recordManageAudit(c, "agent_key.create", map[string]any{
		"name":       key.Name,
		"key_id":     key.Id,
		"scopes":     strings.Join(scopes, ", "),
		"expires_at": key.ExpiresAt,
	})
	common.ApiSuccess(c, key)
}

// ListAgentKeys returns the caller's registered keys.
func ListAgentKeys(c *gin.Context) {
	identity, ok := requireBrowserSession(c)
	if !ok {
		return
	}
	keys, err := model.ListAgentKeys(identity.UserID)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	common.ApiSuccess(c, keys)
}

// RevokeAgentKey disables a key and immediately drops the token it minted.
func RevokeAgentKey(c *gin.Context) {
	identity, ok := requireBrowserSession(c)
	if !ok {
		return
	}
	keyID, err := strconv.Atoi(c.Param("id"))
	if err != nil || keyID <= 0 {
		writeAccessTokenError(c, http.StatusNotFound, "AGENT_KEY_NOT_FOUND", "That key does not exist.")
		return
	}
	proofContext, err := common.Marshal(service.AgentKeyRevokeContext{KeyID: keyID})
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	if middleware.RequireSecurityProof(c, service.VerificationOperation{
		Scope:   service.VerificationScopeAgentKeyRevoke,
		Context: proofContext,
	}) == nil {
		return
	}

	key, err := model.RevokeAgentKey(identity.UserID, keyID)
	if err != nil {
		if errors.Is(err, model.ErrAgentKeyNotFound) {
			writeAccessTokenError(c, http.StatusNotFound, "AGENT_KEY_NOT_FOUND", "That key does not exist.")
			return
		}
		writeSecurityOperationError(c, err)
		return
	}

	recordManageAudit(c, "agent_key.revoke", map[string]any{"name": key.Name, "key_id": key.Id})
	common.ApiSuccess(c, key)
}

// RequestAgentChallenge issues a single-use nonce for a key.
//
// This endpoint is anonymous: knowing a key id gets you a random string, nothing
// more. Minting requires the matching private key, which never leaves the
// client.
func RequestAgentChallenge(c *gin.Context) {
	var req agentChallengeRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil || req.KeyID <= 0 {
		writeAccessTokenError(c, http.StatusBadRequest, "AGENT_CHALLENGE_INVALID", "A valid key_id is required.")
		return
	}

	// Fail fast on an unknown or disabled key so a misconfigured client gets a
	// clear error instead of a challenge it can never redeem.
	key, err := model.GetAgentKeyForExchange(req.KeyID)
	if err != nil {
		writeAccessTokenError(c, http.StatusNotFound, "AGENT_KEY_NOT_FOUND", "That key does not exist.")
		return
	}
	if key.RevokedAt != 0 {
		writeAccessTokenError(c, http.StatusForbidden, "AGENT_KEY_REVOKED", "This key has been revoked.")
		return
	}

	challenge, expiresAt, err := model.IssueAgentChallenge(req.KeyID)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"challenge":  challenge,
		"expires_at": expiresAt,
		"algorithm":  "ed25519",
		"encoding":   "base64",
		// Spelled out in the response because a mismatch between what the server
		// signs and what the client signs is the most common integration failure.
		"sign_payload": "the challenge string, verbatim UTF-8 bytes",
	})
}

// ExchangeAgentSignature trades a signature for a short-lived access token.
func ExchangeAgentSignature(c *gin.Context) {
	var req agentExchangeRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil || req.KeyID <= 0 {
		writeAccessTokenError(c, http.StatusBadRequest, "AGENT_EXCHANGE_INVALID", "key_id, challenge and signature are required.")
		return
	}
	signature := strings.TrimSpace(req.Signature)
	challenge := strings.TrimSpace(req.Challenge)
	if signature == "" || challenge == "" {
		writeAccessTokenError(c, http.StatusBadRequest, "AGENT_EXCHANGE_INVALID", "key_id, challenge and signature are required.")
		return
	}

	key, err := model.GetAgentKeyForExchange(req.KeyID)
	if err != nil {
		writeAccessTokenError(c, http.StatusNotFound, "AGENT_KEY_NOT_FOUND", "That key does not exist.")
		return
	}
	if err := agentKeyUsable(key); err != nil {
		writeAgentKeyStateError(c, err)
		return
	}

	// Consume before verifying: a burned nonce costs the caller one round trip,
	// while a reusable one lets an attacker keep guessing against the same
	// challenge.
	if err := model.ConsumeAgentChallenge(challenge, req.KeyID); err != nil {
		writeAccessTokenError(c, http.StatusBadRequest, "AGENT_CHALLENGE_EXPIRED", "The challenge is invalid, already used, or older than {{seconds}} seconds. Request a new one.", map[string]any{"seconds": model.AgentChallengeTTL})
		return
	}
	if err := model.VerifyAgentSignature(key.PublicKey, challenge, signature); err != nil {
		recordAgentExchangeAudit(c, key, http.StatusUnauthorized, false, "signature")
		writeAccessTokenError(c, http.StatusUnauthorized, "AGENT_SIGNATURE_INVALID", "The signature does not match this key and challenge.")
		return
	}

	scopes := key.GetScopes()
	if len(scopes) == 0 {
		writeAccessTokenError(c, http.StatusForbidden, "AGENT_KEY_SCOPE_EMPTY", "This key has no permissions and cannot be exchanged.")
		return
	}

	// One live token per key: replace rather than accumulate, so repeated
	// handshakes cannot exhaust the personal-token quota.
	if err := model.PurgeAgentKeyTokens(key.Id); err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	suffix, err := common.GenerateRandomCharsKey(43)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	raw := model.AccessTokenPrefix + suffix
	name := agentTokenName(req.Name, key)
	token := &model.UserAccessToken{
		Name:        name,
		TokenHash:   model.AccessTokenFingerprint(raw),
		TokenHint:   model.AccessTokenHint(raw),
		ExpiresAt:   common.GetTimestamp() + model.AgentTokenTTL,
		CreatedAt:   common.GetTimestamp(),
		AuthVersion: key.AuthVersion,
		AgentKeyId:  key.Id,
	}
	if err := token.SetScopes(scopes); err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	if err := model.CreateUserAccessToken(key.UserId, token, service.AccessTokenMaxPerUser); err != nil {
		if errors.Is(err, model.ErrAccessTokenLimit) {
			writeAccessTokenError(c, http.StatusConflict, "AGENT_KEY_TOKEN_QUOTA", "The owner has no free access-token slots. Revoke an unused token and retry.")
			return
		}
		writeSecurityOperationError(c, err)
		return
	}

	if err := model.TouchAgentKey(key.Id, c.ClientIP()); err != nil {
		common.SysError(common.LogText("failed to record agent key usage: %s", err.Error()))
	}
	// A successful exchange mints a credential, so it belongs in the trail even
	// though the resulting token also logs its own use.
	recordAgentExchangeAudit(c, key, http.StatusOK, true, "")

	common.ApiSuccess(c, gin.H{
		"access_token": raw,
		"token_type":   "Bearer",
		"expires_in":   model.AgentTokenTTL,
		"scopes":       scopes,
		"token_ref":    token.TokenHash,
		"agent_key_id": key.Id,
	})
}

// agentKeyUsable applies lifecycle checks that do not involve the signature.
func agentKeyUsable(key *model.AgentKey) error {
	if key.RevokedAt != 0 {
		return model.ErrAgentKeyRevoked
	}
	now := common.GetTimestamp()
	if key.ExpiresAt != 0 && key.ExpiresAt < now {
		return model.ErrAgentKeyExpired
	}
	if key.AuthVersion != 0 {
		var version int64
		if err := model.DB.Model(&model.User{}).Where("id = ?", key.UserId).Select("auth_version").Scan(&version).Error; err != nil {
			return err
		}
		if version != key.AuthVersion {
			return model.ErrAgentKeyStale
		}
	}
	return nil
}

func writeAgentKeyStateError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, model.ErrAgentKeyRevoked):
		writeAccessTokenError(c, http.StatusForbidden, "AGENT_KEY_REVOKED", "This key has been revoked.")
	case errors.Is(err, model.ErrAgentKeyExpired):
		writeAccessTokenError(c, http.StatusForbidden, "AGENT_KEY_EXPIRED", "This key has expired. Register a new one.")
	case errors.Is(err, model.ErrAgentKeyStale):
		writeAccessTokenError(c, http.StatusUnauthorized, "AGENT_KEY_REVOKED", "This key was registered before the last credential change. Register a new one.")
	default:
		writeSecurityOperationError(c, err)
	}
}

// recordAgentExchangeAudit writes one row for a handshake attempt.
//
// It does not go through recordManageAudit because that helper always records
// success: it has no status to pass on, so a rejected signature would land in
// the trail looking like a successful exchange. A burst of failures on one key
// is the signal that someone is trying keys against it, so the status has to be
// truthful.
func recordAgentExchangeAudit(c *gin.Context, key *model.AgentKey, status int, success bool, reason string) {
	params := map[string]any{"key_id": key.Id, "name": key.Name}
	if reason != "" {
		params["reason"] = reason
	}
	action := "agent_key.exchange"
	if !success {
		action = "agent_key.exchange_failed"
	}
	model.RecordOperationAuditLog(
		// Attributed to the key owner: the caller is anonymous, but the key
		// identifies whose credential was used.
		key.UserId,
		0,
		auditContentEN(action, params),
		c.ClientIP(),
		action,
		params,
		nil,
		&model.AuditRequestInfo{
			Method:  c.Request.Method,
			Route:   c.FullPath(),
			Path:    c.Request.URL.Path,
			Status:  status,
			Success: success,
		},
		c,
	)
	markAuditLogged(c)
}

// agentTokenName keeps exchange-minted tokens distinguishable in the token list
// and in audit records.
func agentTokenName(requested string, key *model.AgentKey) string {
	name := strings.TrimSpace(requested)
	if name == "" {
		name = key.Name
	}
	if label, ok := normalizeAccessTokenName("agent:" + name); ok {
		return label
	}
	// A 64-character input plus the prefix would overflow the column.
	runes := []rune("agent:" + name)
	if len(runes) > 64 {
		runes = runes[:64]
	}
	return string(runes)
}

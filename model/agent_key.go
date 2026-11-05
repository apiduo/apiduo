package model

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// AgentKey is a public key registered by an account owner and handed to an
// automated client.
//
// Only the public half is stored, so a database dump yields nothing usable:
// there is no shared secret to steal. Authentication happens by signature, and
// the signature is exchanged for a short-lived access token rather than being
// trusted per request, which keeps the hot path on the existing token
// middleware.
type AgentKey struct {
	Id     int    `json:"id"`
	UserId int    `json:"user_id" gorm:"index"`
	Name   string `json:"name" gorm:"type:varchar(64);not null"`
	// PublicKey holds the base64-encoded 32-byte Ed25519 public key, or a PEM
	// SubjectPublicKeyInfo block. Both are accepted so an operator can paste
	// whatever their tool produced.
	PublicKey string `json:"public_key" gorm:"type:text;not null"`
	KeyType   string `json:"key_type" gorm:"type:varchar(32);default:'ed25519'"`
	// Scopes is the grant this key may exchange for. It is copied onto each
	// issued token, so lowering it here does not retroactively narrow a token
	// that is already out — those expire within AgentTokenTTL.
	Scopes    string `json:"-" gorm:"type:varchar(2048)"`
	ExpiresAt int64  `json:"expires_at" gorm:"type:bigint;default:0"`
	RevokedAt int64  `json:"revoked_at" gorm:"type:bigint;default:0"`
	// AuthVersion mirrors the token binding: a credential change retires the
	// key, so a leaked private key stops working after a password reset.
	AuthVersion int64  `json:"-" gorm:"type:bigint;not null;default:0;column:auth_version"`
	LastUsedAt  int64  `json:"last_used_at" gorm:"type:bigint;default:0"`
	LastUsedIp  string `json:"last_used_ip" gorm:"type:varchar(64)"`
	CreatedAt   int64  `json:"created_at" gorm:"type:bigint"`
}

func (k *AgentKey) TableName() string { return "agent_keys" }

var (
	ErrAgentKeyNotFound   = errors.New("agent key not found")
	ErrAgentKeyRevoked    = errors.New("agent key has been revoked")
	ErrAgentKeyExpired    = errors.New("agent key has expired")
	ErrAgentKeyStale      = errors.New("agent key was registered before the last credential change")
	ErrAgentChallengeUsed = errors.New("agent challenge is invalid or expired")
	ErrAgentSignature     = errors.New("agent signature verification failed")
	ErrAgentPublicKey     = errors.New("agent public key is not a valid Ed25519 key")
)

const (
	// AgentChallengeTTL bounds how long a challenge stays valid. The whole point
	// of a challenge is that a captured signature cannot be replayed, so this is
	// deliberately short: a shell script has to sign and exchange promptly.
	AgentChallengeTTL = 60

	// AgentTokenTTL is the lifetime of the access token issued in exchange for a
	// signature. Fifteen minutes is the trade-off between "leaked token is
	// worthless" and "the client is not re-authenticating constantly". This is
	// the number that makes a copied credential stop mattering.
	AgentTokenTTL = 15 * 60

	// AgentKeyMaxPerUser mirrors the PAT ceiling so one account cannot fill the
	// table.
	AgentKeyMaxPerUser = 20
)

// agentChallenge is a pending nonce. Kept in memory on purpose: it lives for a
// minute, and persisting it would mean a write per handshake for no benefit.
// A multi-instance deployment needs a shared store here, because the challenge
// and the exchange may land on different nodes.
type agentChallenge struct {
	keyID     int
	expiresAt int64
}

var (
	agentChallengeMutex sync.Mutex
	agentChallengeStore = make(map[string]agentChallenge)
)

// IssueAgentChallenge creates a single-use nonce bound to one key.
func IssueAgentChallenge(keyID int) (string, int64, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", 0, err
	}
	challenge := base64.RawURLEncoding.EncodeToString(buf)
	expiresAt := common.GetTimestamp() + AgentChallengeTTL

	agentChallengeMutex.Lock()
	// Opportunistic sweep: the map only ever holds challenges from the last
	// minute, so a full scan is cheaper than a background goroutine.
	now := common.GetTimestamp()
	for value, entry := range agentChallengeStore {
		if entry.expiresAt < now {
			delete(agentChallengeStore, value)
		}
	}
	agentChallengeStore[challenge] = agentChallenge{keyID: keyID, expiresAt: expiresAt}
	agentChallengeMutex.Unlock()

	return challenge, expiresAt, nil
}

// ConsumeAgentChallenge validates and removes a challenge.
//
// Removal happens before the signature is checked so a failed attempt still
// burns the nonce; otherwise an attacker could grind signatures against one
// challenge indefinitely.
func ConsumeAgentChallenge(challenge string, keyID int) error {
	agentChallengeMutex.Lock()
	entry, ok := agentChallengeStore[strings.TrimSpace(challenge)]
	if ok {
		delete(agentChallengeStore, strings.TrimSpace(challenge))
	}
	agentChallengeMutex.Unlock()

	if !ok || entry.keyID != keyID || entry.expiresAt < common.GetTimestamp() {
		return ErrAgentChallengeUsed
	}
	return nil
}

// DecodeAgentPublicKey accepts the two shapes an operator is likely to paste:
// a bare base64 Ed25519 key, or a PEM SubjectPublicKeyInfo block.
func DecodeAgentPublicKey(raw string) (ed25519.PublicKey, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ErrAgentPublicKey
	}

	if strings.HasPrefix(raw, "-----BEGIN") {
		block, _ := pem.Decode([]byte(raw))
		if block == nil {
			return nil, ErrAgentPublicKey
		}
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, ErrAgentPublicKey
		}
		key, ok := parsed.(ed25519.PublicKey)
		if !ok {
			return nil, ErrAgentPublicKey
		}
		return key, nil
	}

	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		// A truncated or URL-safe paste is common enough to try the variants.
		if alt, altErr := base64.RawStdEncoding.DecodeString(raw); altErr == nil {
			decoded = alt
		} else if alt, altErr := base64.RawURLEncoding.DecodeString(raw); altErr == nil {
			decoded = alt
		} else {
			return nil, ErrAgentPublicKey
		}
	}
	if len(decoded) != ed25519.PublicKeySize {
		return nil, ErrAgentPublicKey
	}
	return ed25519.PublicKey(decoded), nil
}

// VerifyAgentSignature checks one signature over one challenge.
func VerifyAgentSignature(publicKey, challenge, signature string) error {
	key, err := DecodeAgentPublicKey(publicKey)
	if err != nil {
		return err
	}

	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signature))
	if err != nil {
		if alt, altErr := base64.RawStdEncoding.DecodeString(strings.TrimSpace(signature)); altErr == nil {
			sig = alt
		} else if alt, altErr := base64.RawURLEncoding.DecodeString(strings.TrimSpace(signature)); altErr == nil {
			sig = alt
		} else {
			return ErrAgentSignature
		}
	}
	if len(sig) != ed25519.SignatureSize {
		return ErrAgentSignature
	}

	// ed25519.Verify is the constant-time comparison; do not hand-roll it.
	if !ed25519.Verify(key, []byte(challenge), sig) {
		return ErrAgentSignature
	}
	return nil
}

// GetScopes returns the stored grant; a malformed value grants nothing, which
// matches the token behaviour rather than failing open.
func (k *AgentKey) GetScopes() []string {
	if strings.TrimSpace(k.Scopes) == "" {
		return nil
	}
	var scopes []string
	if err := common.UnmarshalJsonStr(k.Scopes, &scopes); err != nil {
		return nil
	}
	return scopes
}

// SetScopes stores the grant as JSON.
func (k *AgentKey) SetScopes(scopes []string) error {
	encoded, err := common.Marshal(scopes)
	if err != nil {
		return err
	}
	k.Scopes = string(encoded)
	return nil
}

// ValidateAgentKey applies the lifecycle rules that have nothing to do with the
// signature: existence, revocation, expiry and credential binding.
func ValidateAgentKey(keyID, userID int) (*AgentKey, error) {
	var key AgentKey
	if err := DB.Where("id = ? AND user_id = ?", keyID, userID).First(&key).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAgentKeyNotFound
		}
		return nil, err
	}
	if key.RevokedAt != 0 {
		return nil, ErrAgentKeyRevoked
	}
	now := common.GetTimestamp()
	if key.ExpiresAt != 0 && key.ExpiresAt < now {
		return nil, ErrAgentKeyExpired
	}
	if key.AuthVersion != 0 {
		var version int64
		if err := DB.Model(&User{}).Where("id = ?", key.UserId).Select("auth_version").Scan(&version).Error; err != nil {
			return nil, err
		}
		if version != key.AuthVersion {
			return nil, ErrAgentKeyStale
		}
	}
	return &key, nil
}

// GetAgentKeyForExchange loads a key without a session, for the unauthenticated
// exchange endpoint. Ownership is implied by the signature.
func GetAgentKeyForExchange(keyID int) (*AgentKey, error) {
	var key AgentKey
	if err := DB.Where("id = ?", keyID).First(&key).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAgentKeyNotFound
		}
		return nil, err
	}
	return &key, nil
}

// ListAgentKeys returns the caller's keys. Only the public half is exposed,
// which is safe by definition.
func ListAgentKeys(userID int) ([]AgentKey, error) {
	var keys []AgentKey
	err := DB.Where("user_id = ?", userID).Order("id desc").Find(&keys).Error
	return keys, err
}

// CreateAgentKey registers a public key.
func CreateAgentKey(userID int, key *AgentKey, limit int) error {
	if userID <= 0 || key == nil {
		return ErrAgentKeyNotFound
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&AgentKey{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(limit) {
			return ErrAccessTokenLimit
		}
		key.Id = 0
		key.UserId = userID
		if key.CreatedAt == 0 {
			key.CreatedAt = common.GetTimestamp()
		}
		return tx.Create(key).Error
	})
}

// RevokeAgentKey marks a key unusable and immediately drops the tokens it
// minted. Without the second step a revoked key would keep working until its
// last token expired, which defeats the point of revoking.
func RevokeAgentKey(userID, keyID int) (*AgentKey, error) {
	var key AgentKey
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ? AND user_id = ?", keyID, userID).First(&key).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAgentKeyNotFound
			}
			return err
		}
		if key.RevokedAt == 0 {
			key.RevokedAt = common.GetTimestamp()
			if err := tx.Model(&AgentKey{}).Where("id = ?", key.Id).Update("revoked_at", key.RevokedAt).Error; err != nil {
				return err
			}
		}
		return tx.Where("agent_key_id = ?", key.Id).Delete(&UserAccessToken{}).Error
	})
	if err != nil {
		return nil, err
	}
	return &key, nil
}

// TouchAgentKey records the last successful exchange for leak triage.
func TouchAgentKey(keyID int, ip string) error {
	return DB.Model(&AgentKey{}).Where("id = ?", keyID).Updates(map[string]any{
		"last_used_at": common.GetTimestamp(),
		"last_used_ip": ip,
	}).Error
}

// DeleteExpiredAgentTokens clears tokens minted by exchanges that have already
// lapsed. They are useless anyway; removing them keeps the table from growing
// one row per handshake forever.
func DeleteExpiredAgentTokens() error {
	return DB.Where("agent_key_id > 0 AND expires_at > 0 AND expires_at < ?", common.GetTimestamp()).
		Delete(&UserAccessToken{}).Error
}

// PurgeAgentKeyTokens drops previous tokens for one key before a new one is
// minted, so a key occupies exactly one row regardless of how often it rotates.
func PurgeAgentKeyTokens(keyID int) error {
	return DB.Where("agent_key_id = ?", keyID).Delete(&UserAccessToken{}).Error
}

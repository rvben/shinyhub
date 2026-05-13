package auth

import (
	"crypto/subtle"
	"fmt"
)

// DeployToken is a pre-shared, env-sourced bearer credential that authenticates
// as a fixed synthetic user. The raw token never leaves this struct; callers
// match against its SHA-256 hash (the same hash the DB-backed api_keys path
// uses) so the existing Token auth scheme continues to work unchanged.
type DeployToken struct {
	hash string // hex-encoded sha256 of the raw token
	user *ContextUser
}

// NewDeployToken wraps raw in a DeployToken bound to user. The raw value is
// hashed immediately and discarded; only the hash is retained.
func NewDeployToken(raw string, user *ContextUser) *DeployToken {
	return &DeployToken{hash: HashAPIKey(raw), user: user}
}

// Matches reports whether candidateHash equals the configured token's hash, in
// constant time. candidateHash is the hex SHA-256 of the request's bearer
// value, as produced by HashAPIKey.
func (d *DeployToken) Matches(candidateHash string) bool {
	if d == nil || d.hash == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(d.hash), []byte(candidateHash)) == 1
}

// User returns a fresh copy of the synthetic user this token authenticates as.
// Callers must not mutate the returned struct.
func (d *DeployToken) User() *ContextUser {
	if d == nil || d.user == nil {
		return nil
	}
	u := *d.user
	return &u
}

// deployTokenMinLen is the minimum length for an env-supplied deploy token.
// 32 chars is enough entropy for a random hex/base64/UUID value from any
// reasonable secrets generator (openssl rand -hex 16, uuidgen, etc.).
const deployTokenMinLen = 32

// ValidateDeployTokenFormat enforces a minimum-length floor on the
// SHINYHUB_DEPLOY_TOKEN env var so a typo or placeholder value can't silently
// become a weak credential. The token is opaque: any operator-chosen secret
// (hex, base64, UUID, …) is accepted as long as it meets the length floor.
// API-minted tokens (POST /api/tokens) are generated separately and continue
// to carry the "shk_" prefix for secret-scanner pattern matching.
func ValidateDeployTokenFormat(raw string) error {
	if raw == "" {
		return fmt.Errorf("deploy token is empty")
	}
	if len(raw) < deployTokenMinLen {
		return fmt.Errorf("deploy token must be at least %d characters (got %d); generate one with: openssl rand -hex 32",
			deployTokenMinLen, len(raw))
	}
	return nil
}

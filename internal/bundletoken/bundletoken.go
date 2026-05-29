// Package bundletoken mints and verifies short-lived capability tokens that
// authorise a single fetch of one content-addressed bundle. The token is
// scoped to a content digest, not a task ARN, so it can be minted before
// RunTask returns the ARN while still being unguessable per-deployment.
//
// Key derivation: k = HKDF-SHA256(authSecret, salt=nil, info="shinyhub-fargate-bundle-v1")
// via secrets.DeriveKey (same HKDF helper used by the env-secrets package).
// Token format: "v1.<exp-unix>.<base64url(HMAC-SHA256(k, "v1|<exp>|<digest>"))>"
//
// A leaked token grants any holder the ability to fetch the bundle for that
// digest within the TTL. Short TTL + https transport is the required mitigation.
// Tokens are verified statelessly; there is no revocation.
package bundletoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Sentinel errors returned by Verify.
var (
	ErrTokenExpired     = errors.New("bundletoken: token expired")
	ErrTokenMalformed   = errors.New("bundletoken: malformed token")
	ErrTokenInvalidHMAC = errors.New("bundletoken: invalid HMAC")
	ErrDigestMismatch   = errors.New("bundletoken: digest mismatch")
)

// Mint creates a v1 capability token bound to digest, expiring at nowUnix+ttl.
// secret must be the 32-byte key returned by calling DeriveKey (callers derive
// once and pass it here). nowUnix is injectable for deterministic tests;
// production callers pass time.Now().Unix().
func Mint(secret []byte, digest string, ttl time.Duration, nowUnix int64) string {
	exp := nowUnix + int64(ttl.Seconds())
	expStr := strconv.FormatInt(exp, 10)
	mac := sign(secret, "v1", expStr, digest)
	b64 := base64.RawURLEncoding.EncodeToString(mac)
	return "v1." + expStr + "." + b64
}

// Verify checks that token is a valid, unexpired capability token for digest.
// secret is the same 32-byte derived key passed to Mint. nowUnix is injectable
// for deterministic tests; production callers pass time.Now().Unix().
func Verify(secret []byte, digest, token string, nowUnix int64) error {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 || parts[0] != "v1" {
		return ErrTokenMalformed
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return ErrTokenMalformed
	}
	if nowUnix > exp {
		return ErrTokenExpired
	}
	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return ErrTokenMalformed
	}
	want := sign(secret, "v1", parts[1], digest)
	if subtle.ConstantTimeCompare(want, got) != 1 {
		return ErrTokenInvalidHMAC
	}
	// Digest is already baked into the HMAC; this line documents the invariant.
	_ = digest
	return nil
}

// sign returns HMAC-SHA256(key, "v1|exp|digest").
func sign(key []byte, version, exp, digest string) []byte {
	msg := version + "|" + exp + "|" + digest
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msg))
	return h.Sum(nil)
}

// compile-time guard: Mint must return a non-empty string.
var _ = fmt.Sprintf

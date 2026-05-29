package bundletoken_test

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/bundletoken"
)

var testSecret = []byte("aaaabbbbccccddddeeeeffffgggghhhh") // 32 bytes

func TestMintVerify_Valid(t *testing.T) {
	now := int64(1_000_000)
	tok := bundletoken.Mint(testSecret, "sha256:abc123", 10*time.Minute, now)
	if err := bundletoken.Verify(testSecret, "sha256:abc123", tok, now+1); err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
}

func TestVerify_Expired(t *testing.T) {
	now := int64(1_000_000)
	tok := bundletoken.Mint(testSecret, "sha256:abc123", 10*time.Minute, now)
	err := bundletoken.Verify(testSecret, "sha256:abc123", tok, now+601) // past 10 min TTL
	if err != bundletoken.ErrTokenExpired {
		t.Fatalf("want ErrTokenExpired, got %v", err)
	}
}

func TestVerify_WrongDigest(t *testing.T) {
	now := int64(1_000_000)
	tok := bundletoken.Mint(testSecret, "sha256:abc123", 10*time.Minute, now)
	// A different digest must fail HMAC verification, not a separate digest-check.
	err := bundletoken.Verify(testSecret, "sha256:different", tok, now+1)
	if err != bundletoken.ErrTokenInvalidHMAC {
		t.Fatalf("want ErrTokenInvalidHMAC for wrong digest, got %v", err)
	}
}

func TestVerify_Tampered(t *testing.T) {
	now := int64(1_000_000)
	tok := bundletoken.Mint(testSecret, "sha256:abc123", 10*time.Minute, now)
	// Corrupt the last byte of the base64 signature.
	runes := []rune(tok)
	runes[len(runes)-1] ^= 1
	err := bundletoken.Verify(testSecret, "sha256:abc123", string(runes), now+1)
	if err == nil {
		t.Fatal("expected error for tampered token, got nil")
	}
}

func TestVerify_Empty(t *testing.T) {
	err := bundletoken.Verify(testSecret, "sha256:abc123", "", int64(1_000_000))
	if err != bundletoken.ErrTokenMalformed {
		t.Fatalf("want ErrTokenMalformed for empty token, got %v", err)
	}
}

func TestVerify_MalformedOnePart(t *testing.T) {
	err := bundletoken.Verify(testSecret, "digest", "notavalidtoken", int64(1_000_000))
	if err != bundletoken.ErrTokenMalformed {
		t.Fatalf("want ErrTokenMalformed, got %v", err)
	}
}

func TestVerify_WrongVersion(t *testing.T) {
	// A v0 prefix must be rejected as malformed.
	err := bundletoken.Verify(testSecret, "digest", "v0.1234567890.abc", int64(1_000_000))
	if err != bundletoken.ErrTokenMalformed {
		t.Fatalf("want ErrTokenMalformed for wrong version, got %v", err)
	}
}

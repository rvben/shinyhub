package secrets_test

import (
	"bytes"
	"testing"

	"github.com/rvben/shinyhub/internal/secrets"
)

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	key := secrets.DeriveKey("super-secret-auth-token-value")
	plaintext := []byte("hunter2")

	ct, err := secrets.Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Contains(ct, plaintext) {
		t.Fatal("ciphertext contains plaintext")
	}

	pt, err := secrets.Decrypt(key, ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(pt, plaintext) {
		t.Errorf("got %q, want %q", pt, plaintext)
	}
}

func TestEncrypt_NonceIsUnique(t *testing.T) {
	key := secrets.DeriveKey("auth-secret")
	a, _ := secrets.Encrypt(key, []byte("same"))
	b, _ := secrets.Encrypt(key, []byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("identical plaintext produced identical ciphertext — nonce not unique")
	}
}

func TestDecrypt_TamperedCiphertextRejected(t *testing.T) {
	key := secrets.DeriveKey("auth-secret")
	ct, _ := secrets.Encrypt(key, []byte("hello"))
	ct[len(ct)-1] ^= 0x01 // flip a bit in the tag
	if _, err := secrets.Decrypt(key, ct); err == nil {
		t.Fatal("expected error from tampered ciphertext, got nil")
	}
}

func TestDecrypt_WrongKeyRejected(t *testing.T) {
	k1 := secrets.DeriveKey("auth-secret-a")
	k2 := secrets.DeriveKey("auth-secret-b")
	ct, _ := secrets.Encrypt(k1, []byte("hello"))
	if _, err := secrets.Decrypt(k2, ct); err == nil {
		t.Fatal("expected error decrypting with wrong key")
	}
}

func TestDeriveKey_StableAcrossCalls(t *testing.T) {
	a := secrets.DeriveKey("x")
	b := secrets.DeriveKey("x")
	if !bytes.Equal(a, b) {
		t.Error("DeriveKey not deterministic")
	}
}

func TestDeriveKey_Length32(t *testing.T) {
	k := secrets.DeriveKey("x")
	if len(k) != 32 {
		t.Errorf("want 32-byte key, got %d", len(k))
	}
}

func TestDeriveKeyWithInfo_DomainSeparation(t *testing.T) {
	secret := "the-auth-secret"
	envKey := secrets.DeriveKeyWithInfo(secret, "shinyhub-app-env-v1")
	caKey := secrets.DeriveKeyWithInfo(secret, "shinyhub-worker-ca-v1")
	if bytes.Equal(envKey, caKey) {
		t.Fatal("different info strings must derive different keys")
	}
	if len(caKey) != 32 {
		t.Fatalf("key len = %d, want 32", len(caKey))
	}
	// DeriveKey must equal the env-var info derivation (back-compat).
	if !bytes.Equal(secrets.DeriveKey(secret), envKey) {
		t.Fatal("DeriveKey must match DeriveKeyWithInfo(secret, app-env-v1)")
	}
	// A value encrypted under the CA key does not decrypt under the env key.
	ct, err := secrets.Encrypt(caKey, []byte("secret-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Decrypt(envKey, ct); err == nil {
		t.Fatal("env key must not decrypt CA-key ciphertext")
	}
}

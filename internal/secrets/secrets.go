// Package secrets encrypts and decrypts small values (env var secrets)
// using AES-256-GCM with a key derived from the server's auth secret via
// HKDF-SHA256. A ciphertext is a single byte slice: nonce || ct || tag.
//
// Rotating the auth secret invalidates all existing ciphertexts; callers
// must surface decrypt errors clearly so operators know to re-set affected
// values.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"

	"golang.org/x/crypto/hkdf"
)

// infoString is the HKDF info parameter. The -v1 suffix reserves version space
// for future key derivation migrations.
const infoString = "shinyhub-app-env-v1"

// DeriveKey returns a 32-byte AES-256 key from the given auth secret.
// Deterministic for a given input; safe to call repeatedly.
func DeriveKey(authSecret string) []byte {
	r := hkdf.New(sha256.New, []byte(authSecret), nil, []byte(infoString))
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		panic(err) // HKDF-SHA256 read of 32 bytes cannot fail
	}
	return key
}

// Encrypt returns nonce || ciphertext || tag.
func Encrypt(key, plaintext []byte) ([]byte, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt reverses Encrypt. Returns an error if the input is truncated,
// the key is wrong, or the ciphertext has been tampered with.
func Decrypt(key, blob []byte) ([]byte, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	if len(blob) < aead.NonceSize() {
		return nil, errors.New("secrets: ciphertext too short")
	}
	nonce, ct := blob[:aead.NonceSize()], blob[aead.NonceSize():]
	return aead.Open(nil, nonce, ct, nil)
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

package db_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/rvben/shinyhub/internal/secrets"
)

// TestRotateSecretsTx_ReEncryptsEnvAndCA verifies rotation re-encrypts every
// at-rest secret (app_env_vars is_secret values + the worker CA key) from an old
// key to a new one atomically: afterward the new key decrypts them and the old
// key does not, non-secret vars are untouched, and the plaintext is preserved.
func TestRotateSecretsTx_ReEncryptsEnvAndCA(t *testing.T) {
	s := openTestStore(t)
	owner := mustCreateUser(t, s, "owner", "admin")
	app := mustCreateApp(t, s, "demo", owner.ID)

	oldKey := secrets.DeriveKey("old-secret-old-secret-old-secret-32")
	newKey := secrets.DeriveKey("new-secret-new-secret-new-secret-32")
	oldCA := secrets.DeriveKeyWithInfo("old-secret-old-secret-old-secret-32", "ca")
	newCA := secrets.DeriveKeyWithInfo("new-secret-new-secret-new-secret-32", "ca")

	// Two secret env vars + one plaintext (non-secret) var.
	seedSecret := func(key, plain string) {
		ct, err := secrets.Encrypt(oldKey, []byte(plain))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.UpsertAppEnvVar(app.ID, key, ct, true); err != nil {
			t.Fatal(err)
		}
	}
	seedSecret("API_KEY", "sk-123")
	seedSecret("DB_PASS", "hunter2")
	if err := s.UpsertAppEnvVar(app.ID, "PUBLIC", []byte("plainvalue"), false); err != nil {
		t.Fatal(err)
	}

	// A worker CA key encrypted under the old CA KEK.
	caPlain := []byte("PRETEND-CA-PRIVATE-KEY-PEM")
	caEnc, err := secrets.Encrypt(oldCA, caPlain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutWorkerCAIfAbsent([]byte("CERT"), caEnc); err != nil {
		t.Fatal(err)
	}

	reEnv := func(old []byte) ([]byte, error) {
		p, err := secrets.Decrypt(oldKey, old)
		if err != nil {
			return nil, err
		}
		return secrets.Encrypt(newKey, p)
	}
	reCA := func(old []byte) ([]byte, error) {
		p, err := secrets.Decrypt(oldCA, old)
		if err != nil {
			return nil, err
		}
		return secrets.Encrypt(newCA, p)
	}

	n, caRotated, err := s.RotateSecretsTx(reEnv, reCA)
	if err != nil {
		t.Fatalf("RotateSecretsTx: %v", err)
	}
	if n != 2 {
		t.Errorf("rotated env count = %d, want 2 (the secret vars only)", n)
	}
	if !caRotated {
		t.Error("worker CA should have been rotated")
	}

	// Secret vars now decrypt with the NEW key, not the old.
	for key, want := range map[string]string{"API_KEY": "sk-123", "DB_PASS": "hunter2"} {
		v, err := s.GetAppEnvVar(app.ID, key)
		if err != nil {
			t.Fatalf("get %s: %v", key, err)
		}
		if _, err := secrets.Decrypt(oldKey, v.Value); err == nil {
			t.Errorf("%s must NOT decrypt with the old key after rotation", key)
		}
		got, err := secrets.Decrypt(newKey, v.Value)
		if err != nil {
			t.Fatalf("%s must decrypt with the new key: %v", key, err)
		}
		if string(got) != want {
			t.Errorf("%s plaintext = %q, want %q", key, got, want)
		}
	}

	// Non-secret var is untouched.
	pub, _ := s.GetAppEnvVar(app.ID, "PUBLIC")
	if !bytes.Equal(pub.Value, []byte("plainvalue")) {
		t.Errorf("non-secret var must be untouched, got %q", pub.Value)
	}

	// Worker CA now decrypts with the new CA KEK.
	_, keyEnc, _, _ := s.GetWorkerCA()
	got, err := secrets.Decrypt(newCA, keyEnc)
	if err != nil || !bytes.Equal(got, caPlain) {
		t.Errorf("worker CA must decrypt with the new KEK to the original key, err=%v", err)
	}
}

// TestRotateSecretsTx_AtomicOnError verifies a mid-rotation failure rolls
// everything back: no secret is left re-encrypted under the new key while the
// rest remain old (which would be unrecoverable).
func TestRotateSecretsTx_AtomicOnError(t *testing.T) {
	s := openTestStore(t)
	owner := mustCreateUser(t, s, "owner", "admin")
	app := mustCreateApp(t, s, "demo", owner.ID)

	oldKey := secrets.DeriveKey("old-secret-old-secret-old-secret-32")
	newKey := secrets.DeriveKey("new-secret-new-secret-new-secret-32")

	for _, kv := range [][2]string{{"A", "aaa"}, {"B", "bbb"}} {
		ct, _ := secrets.Encrypt(oldKey, []byte(kv[1]))
		if err := s.UpsertAppEnvVar(app.ID, kv[0], ct, true); err != nil {
			t.Fatal(err)
		}
	}

	// A re-encrypt func that fails on the second value.
	calls := 0
	reEnv := func(old []byte) ([]byte, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("boom")
		}
		p, _ := secrets.Decrypt(oldKey, old)
		return secrets.Encrypt(newKey, p)
	}
	reCA := func(old []byte) ([]byte, error) { return old, nil }

	if _, _, err := s.RotateSecretsTx(reEnv, reCA); err == nil {
		t.Fatal("expected rotation to fail")
	}
	// Both vars must still decrypt with the OLD key (rollback preserved them).
	for _, key := range []string{"A", "B"} {
		v, _ := s.GetAppEnvVar(app.ID, key)
		if _, err := secrets.Decrypt(oldKey, v.Value); err != nil {
			t.Errorf("%s must still decrypt with the old key after a rolled-back rotation: %v", key, err)
		}
	}
}

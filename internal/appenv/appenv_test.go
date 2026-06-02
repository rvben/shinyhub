package appenv_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/appenv"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/secrets"
)

// encSecret encrypts plaintext under the test key so a stored AppEnvVar mirrors
// what the DB holds for a secret env var (ciphertext bytes, IsSecret=true).
func encSecret(t *testing.T, key []byte, plaintext string) []byte {
	t.Helper()
	ct, err := secrets.Encrypt(key, []byte(plaintext))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	return ct
}

func TestResolve_NonSecretGoesToEnvAsPlaintext(t *testing.T) {
	key := secrets.DeriveKey("test-auth-secret")
	vars := []db.AppEnvVar{
		{Key: "AWS_REGION", Value: []byte("eu-west-1"), IsSecret: false},
	}

	env, secretEnv, err := appenv.Resolve(vars, key)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(secretEnv) != 0 {
		t.Errorf("non-secret var must not appear in secretEnv: %v", secretEnv)
	}
	if len(env) != 1 || env[0] != "AWS_REGION=eu-west-1" {
		t.Errorf("env = %v, want [AWS_REGION=eu-west-1]", env)
	}
}

func TestResolve_SecretIsDecryptedIntoSecretEnv(t *testing.T) {
	key := secrets.DeriveKey("test-auth-secret")
	vars := []db.AppEnvVar{
		{Key: "AWS_SECRET", Value: encSecret(t, key, "super-secret-value"), IsSecret: true},
	}

	env, secretEnv, err := appenv.Resolve(vars, key)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(env) != 0 {
		t.Errorf("secret var must not appear in env: %v", env)
	}
	if len(secretEnv) != 1 || secretEnv[0] != "AWS_SECRET=super-secret-value" {
		t.Errorf("secretEnv = %v, want [AWS_SECRET=super-secret-value]", secretEnv)
	}
}

func TestResolve_PartitionsMixedVarsPreservingOrder(t *testing.T) {
	key := secrets.DeriveKey("test-auth-secret")
	vars := []db.AppEnvVar{
		{Key: "A_PLAIN", Value: []byte("1"), IsSecret: false},
		{Key: "B_SECRET", Value: encSecret(t, key, "b-val"), IsSecret: true},
		{Key: "C_PLAIN", Value: []byte("3"), IsSecret: false},
		{Key: "D_SECRET", Value: encSecret(t, key, "d-val"), IsSecret: true},
	}

	env, secretEnv, err := appenv.Resolve(vars, key)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	wantEnv := []string{"A_PLAIN=1", "C_PLAIN=3"}
	if len(env) != len(wantEnv) {
		t.Fatalf("env = %v, want %v", env, wantEnv)
	}
	for i := range wantEnv {
		if env[i] != wantEnv[i] {
			t.Errorf("env[%d] = %q, want %q", i, env[i], wantEnv[i])
		}
	}

	wantSecret := []string{"B_SECRET=b-val", "D_SECRET=d-val"}
	if len(secretEnv) != len(wantSecret) {
		t.Fatalf("secretEnv = %v, want %v", secretEnv, wantSecret)
	}
	for i := range wantSecret {
		if secretEnv[i] != wantSecret[i] {
			t.Errorf("secretEnv[%d] = %q, want %q", i, secretEnv[i], wantSecret[i])
		}
	}
}

func TestResolve_DecryptFailureFailsClosed(t *testing.T) {
	key := secrets.DeriveKey("the-right-secret")
	wrongKey := secrets.DeriveKey("a-different-secret")
	vars := []db.AppEnvVar{
		{Key: "TOKEN", Value: encSecret(t, key, "value"), IsSecret: true},
	}

	env, secretEnv, err := appenv.Resolve(vars, wrongKey)
	if err == nil {
		t.Fatalf("expected error when a secret cannot be decrypted, got env=%v secretEnv=%v", env, secretEnv)
	}
	if env != nil || secretEnv != nil {
		t.Errorf("on failure both slices must be nil, got env=%v secretEnv=%v", env, secretEnv)
	}
}

func TestResolve_EmptyInput(t *testing.T) {
	env, secretEnv, err := appenv.Resolve(nil, secrets.DeriveKey("k"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(env) != 0 || len(secretEnv) != 0 {
		t.Errorf("empty input should yield empty slices, got env=%v secretEnv=%v", env, secretEnv)
	}
}

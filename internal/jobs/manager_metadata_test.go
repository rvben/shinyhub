package jobs_test

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/jobs"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/secrets"
)

// TestManager_Run_SplitsSecretEnvFromPlainEnv guards that scheduled runs route
// decrypted secret env vars into StartParams.SecretEnv and non-secret vars into
// StartParams.Env, so the runtime can deliver secrets out of band. A secret
// value must never appear in the plaintext Env slice.
func TestManager_Run_SplitsSecretEnvFromPlainEnv(t *testing.T) {
	key := secrets.DeriveKey("test-auth-secret")
	encVal, err := secrets.Encrypt(key, []byte("super-secret"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	rt := &fakeRuntime{exitInfo: process.ExitInfo{Code: 0}}
	st := newFakeStore(makeSchedule("concurrent", 30), makeApp())
	st.envVars = []db.AppEnvVar{
		{Key: "AWS_REGION", Value: []byte("eu-west-1"), IsSecret: false},
		{Key: "AWS_SECRET", Value: encVal, IsSecret: true},
	}

	dir := t.TempDir()
	pm := process.NewManager(dir, rt)
	m, err := jobs.NewManager(pm, nil, process.DefaultTier, st, key, dir, dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := m.Run(1, "manual", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitForCalls(t, rt, 1, 2*time.Second)

	rt.mu.Lock()
	got := rt.lastParams
	rt.mu.Unlock()

	if got.AppID != 10 {
		t.Errorf("StartParams.AppID = %d, want 10 (makeApp id) for per-app secret naming", got.AppID)
	}
	if !contains(got.Env, "AWS_REGION=eu-west-1") {
		t.Errorf("non-secret AWS_REGION missing from Env: %v", got.Env)
	}
	if contains(got.Env, "AWS_SECRET=super-secret") {
		t.Errorf("secret value leaked into plaintext Env: %v", got.Env)
	}
	if !contains(got.SecretEnv, "AWS_SECRET=super-secret") {
		t.Errorf("decrypted secret missing from SecretEnv: %v", got.SecretEnv)
	}
	if contains(got.SecretEnv, "AWS_REGION=eu-west-1") {
		t.Errorf("non-secret value leaked into SecretEnv: %v", got.SecretEnv)
	}
}

func contains(s []string, want string) bool {
	for _, e := range s {
		if e == want {
			return true
		}
	}
	return false
}

// TestManager_Run_PassesDeploymentMetadataToRuntime guards that scheduled runs
// stamp the live deployment's content digest, version, and id onto StartParams
// so a remote job runner can pull-by-digest and label the run. It drives the
// real run path and inspects the StartParams the runtime received.
func TestManager_Run_PassesDeploymentMetadataToRuntime(t *testing.T) {
	rt := &fakeRuntime{exitInfo: process.ExitInfo{Code: 0}}
	st := newFakeStore(makeSchedule("concurrent", 30), makeApp())
	// Seed the live deployment with known metadata (newest-first). AppID 10
	// matches the value makeApp() produces.
	st.deployments = []*db.Deployment{
		{ID: 42, AppID: 10, Version: "v9", ContentDigest: "sha256:job", BundleDir: "/tmp/fake-bundle"},
	}
	dir := t.TempDir()
	pm := process.NewManager(dir, rt)
	m, err := jobs.NewManager(pm, nil, process.DefaultTier, st, nil, dir, dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := m.Run(1, "manual", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitForCalls(t, rt, 1, 2*time.Second)

	rt.mu.Lock()
	got := rt.lastParams
	rt.mu.Unlock()

	if got.ContentDigest != "sha256:job" {
		t.Errorf("StartParams.ContentDigest = %q, want %q", got.ContentDigest, "sha256:job")
	}
	if got.AppVersion != "v9" {
		t.Errorf("StartParams.AppVersion = %q, want %q", got.AppVersion, "v9")
	}
	if got.DeploymentID != 42 {
		t.Errorf("StartParams.DeploymentID = %d, want 42", got.DeploymentID)
	}
}

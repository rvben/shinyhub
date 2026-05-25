package jobs_test

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/jobs"
	"github.com/rvben/shinyhub/internal/process"
)

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
	m, err := jobs.NewManager(rt, st, nil, dir, dir)
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

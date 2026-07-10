package api

import (
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// DeployInFlight must report exactly the window in which the per-slug deploy
// lock is held: the proxy's miss-status lookup uses it to tell a live deploy
// window apart from a stale pending deployment row, so a false positive would
// mask a stopped/crashed app behind the deploying page and a false negative
// would show the stopped page mid-deploy.
func TestDeployInFlight_TracksLockWindow(t *testing.T) {
	srv := New(&config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir()},
	}, dbtest.New(t), nil, nil)

	if srv.DeployInFlight("demo") {
		t.Fatal("in-flight reported before any lock was acquired")
	}
	release := srv.acquireDeployLock("demo")
	if !srv.DeployInFlight("demo") {
		t.Fatal("in-flight not reported while the deploy lock is held")
	}
	if srv.DeployInFlight("other") {
		t.Fatal("unrelated slug reported in-flight")
	}
	release()
	if srv.DeployInFlight("demo") {
		t.Fatal("in-flight still reported after release")
	}
}

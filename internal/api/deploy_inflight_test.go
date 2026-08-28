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

func TestTryAcquireAppOperation_UsesTheDeployMutexPerSlug(t *testing.T) {
	srv := New(&config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir()},
	}, dbtest.New(t), nil, nil)

	release := srv.acquireDeployLock("demo")
	if backgroundRelease, ok := srv.TryAcquireAppOperation("demo"); ok {
		backgroundRelease()
		t.Fatal("background operation acquired a slug held by deploy")
	}
	otherRelease, ok := srv.TryAcquireAppOperation("other")
	if !ok {
		t.Fatal("unrelated slug was unnecessarily excluded")
	}
	otherRelease()
	release()

	backgroundRelease, ok := srv.TryAcquireAppOperation("demo")
	if !ok {
		t.Fatal("background operation could not acquire released slug")
	}
	backgroundRelease()
}

func TestTryAcquireAppOperation_UsesFilesystemFenceAcrossServers(t *testing.T) {
	appsDir := t.TempDir()
	newServer := func() *Server {
		return New(&config.Config{
			Auth:    config.AuthConfig{Secret: "test-secret"},
			Storage: config.StorageConfig{AppsDir: appsDir},
		}, dbtest.New(t), nil, nil)
	}
	first := newServer()
	second := newServer()

	release := first.acquireDeployLock("demo")
	if competingRelease, ok := second.TryAcquireAppOperation("demo"); ok {
		competingRelease()
		release()
		t.Fatal("second server acquired lifecycle operation while first server held filesystem fence")
	}
	otherRelease, ok := second.TryAcquireAppOperation("other")
	if !ok {
		release()
		t.Fatal("filesystem fence unnecessarily serialized an unrelated app")
	}
	otherRelease()
	release()

	competingRelease, ok := second.TryAcquireAppOperation("demo")
	if !ok {
		t.Fatal("second server could not acquire lifecycle operation after filesystem fence release")
	}
	competingRelease()
}

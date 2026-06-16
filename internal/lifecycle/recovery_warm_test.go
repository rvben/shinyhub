package lifecycle

import (
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func seedWarmApp(t *testing.T) (*db.Store, *db.App) {
	t.Helper()
	store := dbtest.New(t)
	hash, _ := auth.HashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "demo", OwnerID: 1}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("demo")
	if err != nil {
		t.Fatal(err)
	}
	return store, app
}

func replicaAt(t *testing.T, store *db.Store, appID int64, idx int) *db.Replica {
	t.Helper()
	reps, err := store.ListReplicas(appID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reps {
		if r.Index == idx {
			return r
		}
	}
	t.Fatalf("no replica row at index %d", idx)
	return nil
}

// TestCleanupFrozenWarmReplica_DowngradesRow: a suspended/warm row with no live
// process is downgraded to stopped/warm so a later expansion cold-boots the slot.
func TestCleanupFrozenWarmReplica_DowngradesRow(t *testing.T) {
	store, app := seedWarmApp(t)
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 1, Status: "suspended", Provider: "native",
		Tier: "default", DesiredState: db.ReplicaDesiredWarm,
	}); err != nil {
		t.Fatal(err)
	}

	cleanupFrozenWarmReplica(store, app, replicaAt(t, store, app.ID, 1), t.TempDir())

	r := replicaAt(t, store, app.ID, 1)
	if r.Status != "stopped" || r.DesiredState != db.ReplicaDesiredWarm {
		t.Fatalf("row = %s/%s, want stopped/%s", r.Status, r.DesiredState, db.ReplicaDesiredWarm)
	}
}

// TestCleanupFrozenWarmReplica_ReapsMatchingFrozenProcess: a SIGSTOP-frozen
// process whose cwd matches the bundle dir is SIGKILLed (preventing the
// frozen-process leak) and its row downgraded. Linux-only: the identity check
// reads the cwd from /proc, which gopsutil cannot do reliably elsewhere.
func TestCleanupFrozenWarmReplica_ReapsMatchingFrozenProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("frozen-process cwd identity check relies on /proc (linux)")
	}
	store, app := seedWarmApp(t)
	bundleDir := t.TempDir()
	cmd := exec.Command("sleep", "60")
	cmd.Dir = bundleDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGSTOP) // freeze
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })

	port := 0
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 1, PID: &pid, Port: &port, Status: "suspended",
		Provider: "native", Tier: "default", DesiredState: db.ReplicaDesiredWarm,
	}); err != nil {
		t.Fatal(err)
	}

	cleanupFrozenWarmReplica(store, app, replicaAt(t, store, app.ID, 1), bundleDir)

	// The frozen process must have been reaped: cmd.Wait returns promptly.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done: // exited (killed) - good
	case <-time.After(3 * time.Second):
		t.Fatal("frozen process not reaped within 3s; cleanup failed to kill it")
	}

	r := replicaAt(t, store, app.ID, 1)
	if r.Status != "stopped" || r.DesiredState != db.ReplicaDesiredWarm {
		t.Errorf("row = %s/%s, want stopped/%s", r.Status, r.DesiredState, db.ReplicaDesiredWarm)
	}
}

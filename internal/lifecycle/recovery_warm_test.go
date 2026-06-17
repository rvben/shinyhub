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
	"github.com/rvben/shinyhub/internal/process"
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

// TestRecoverNativeReplica_ReAdoptsFrozenWarmReplica: a SIGSTOP-frozen warm
// replica whose cwd matches the bundle dir is re-adopted warm (NOT reaped) -
// registered in the Manager as suspended so its next wake SIGCONT-resumes it.
// Linux-only: the identity check reads cwd from /proc.
func TestRecoverNativeReplica_ReAdoptsFrozenWarmReplica(t *testing.T) {
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

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	if !recoverNativeReplica(store, mgr, nil, app, replicaAt(t, store, app.ID, 1), bundleDir) {
		t.Fatalf("a verified frozen warm replica must be re-adopted, not reaped")
	}
	// The frozen process must NOT have been killed - it stays frozen, warm-resumable.
	if syscall.Kill(pid, 0) != nil {
		t.Fatalf("re-adopt must not kill the frozen process")
	}
	// The Manager holds it as a suspended (warm-resumable) slot.
	info, ok := mgr.GetReplica(app.Slug, 1)
	if !ok || info.Status != process.StatusSuspended {
		t.Fatalf("manager slot = %+v ok=%v, want suspended", info, ok)
	}
}

// TestRecoverNativeReplica_FrozenWithNoProcessDowngrades: a frozen warm replica
// whose process is gone (no PID) cannot be re-adopted, so recovery falls back to
// the reap/downgrade path. Runs everywhere (no live process / /proc needed).
func TestRecoverNativeReplica_FrozenWithNoProcessDowngrades(t *testing.T) {
	store, app := seedWarmApp(t)
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 1, Status: "suspended", Provider: "native",
		Tier: "default", DesiredState: db.ReplicaDesiredWarm,
	}); err != nil {
		t.Fatal(err)
	}
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())

	if recoverNativeReplica(store, mgr, nil, app, replicaAt(t, store, app.ID, 1), t.TempDir()) {
		t.Fatalf("a frozen warm replica with no live process must not be re-adopted")
	}
	if r := replicaAt(t, store, app.ID, 1); r.Status != "stopped" {
		t.Fatalf("row = %s, want stopped (downgraded after failed re-adopt)", r.Status)
	}
}

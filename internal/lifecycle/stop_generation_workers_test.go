package lifecycle

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	gops "github.com/shirou/gopsutil/v4/process"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
)

type bundleLookup string

func (b bundleLookup) GetDeploymentByID(id int64) (*db.Deployment, error) {
	return &db.Deployment{ID: id, BundleDir: string(b)}, nil
}

// foreignPID returns a live PID above 1 that this user may not signal, as a
// process or as a group.
func foreignPID(t *testing.T) int {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("as root every PID can be signalled, so no process is another user's")
	}
	pids, err := gops.Pids()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pids {
		pid := int(p)
		if pid <= 1 || syscall.Kill(pid, 0) != syscall.EPERM {
			continue
		}
		if err := syscall.Kill(-pid, 0); err == syscall.EPERM || err == syscall.ESRCH {
			return pid
		}
	}
	t.Skip("no process of another user on this host")
	return 0
}

// startForkedWorker starts a process group in dir as NativeRuntime starts a
// worker and reaps it on exit, so the returned channel closes when it ends.
func startForkedWorker(t *testing.T, dir string) (int, <-chan struct{}) {
	t.Helper()
	cmd := exec.Command("sleep", "120")
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	})
	return cmd.Process.Pid, done
}

// forkedRow is a ledger row as NativeRuntime records a worker it forked: the
// worker's PID doubles as its WorkerID.
func forkedRow(index, pid int, tier string) *db.DeploymentReplica {
	return &db.DeploymentReplica{DeploymentID: 1, Index: index, PID: &pid, Provider: "native", Tier: tier, WorkerID: strconv.Itoa(pid)}
}

// EPERM proves a PID is not a worker only for a worker the server forked
// itself, which runs as the server's own user. A worker a systemd unit runs
// under a per-app uid answers EPERM while alive, so a unit row on a tier no
// longer mapped to systemd must stay unconfirmed rather than be forgotten.
func TestStopGenerationWorkers_EPERMConfirmsOnlyAServerForkedWorker(t *testing.T) {
	pid := foreignPID(t)
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	app := &db.App{Slug: "eperm"}
	forked := forkedRow(0, pid, "host")
	legacy := &db.DeploymentReplica{DeploymentID: 1, Index: 1, PID: &pid, Provider: "native", Tier: "host"}
	unit := &db.DeploymentReplica{DeploymentID: 1, Index: 2, PID: &pid, Provider: "native", Tier: "isolated", WorkerID: "shinyhub-app-eperm-2.service"}

	unconfirmed := StopGenerationWorkers(bundleLookup(t.TempDir()), mgr, app, []*db.DeploymentReplica{forked, legacy, unit})

	if len(unconfirmed) != 1 || unconfirmed[0] != unit {
		t.Fatalf("unconfirmed = %+v, want only the unit row: a forked worker's PID that answers EPERM "+
			"belongs to another user, a unit worker's may be the worker itself", unconfirmed)
	}
}

// A forked worker's row whose tier has since been mapped to systemd is still a
// process group the server forked. Its WorkerID is a PID, not a unit name, so
// asking the broker to stop it can never succeed; it must be stopped as the
// native process it is.
func TestStopGenerationWorkers_StopsAForkedWorkerOnARemappedSystemdTier(t *testing.T) {
	bundle := t.TempDir()
	pid, done := startForkedWorker(t, bundle)
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.RegisterRuntime("isolated", &process.SystemdRuntime{})

	unconfirmed := StopGenerationWorkers(bundleLookup(bundle), mgr, &db.App{Slug: "remap"}, []*db.DeploymentReplica{forkedRow(0, pid, "isolated")})

	if len(unconfirmed) != 0 {
		t.Fatalf("unconfirmed = %+v, want the forked worker stopped as a native process group", unconfirmed)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stop was confirmed but the forked worker is still running")
	}
}

// A recorded PID of 1 can never be a worker, and signalling its "group" would
// signal every process this user may signal.
func TestStopGenerationWorkers_NeverSignalsPIDOne(t *testing.T) {
	one := 1
	row := &db.DeploymentReplica{DeploymentID: 1, PID: &one, Provider: "native", Tier: "host", WorkerID: "1"}
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())

	// The bundle is never init's working directory, so even as root a missing
	// guard ends in a refusal rather than in kill(-1, SIGTERM).
	if unconfirmed := StopGenerationWorkers(bundleLookup(t.TempDir()), mgr, &db.App{Slug: "init"}, []*db.DeploymentReplica{row}); len(unconfirmed) != 0 {
		t.Fatalf("unconfirmed = %+v, want a PID-1 row confirmed without a signal", unconfirmed)
	}
}

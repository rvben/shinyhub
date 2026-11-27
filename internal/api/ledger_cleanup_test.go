package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
	gops "github.com/shirou/gopsutil/v4/process"
)

// ledgerOrphan is a process recorded in the generation ledger that the
// manager does not run: a worker left behind by an earlier server process.
type ledgerOrphan struct {
	pid    int
	exited chan struct{}
}

// startLedgerOrphan starts a process group in dir, as NativeRuntime starts a
// worker, and reaps it as soon as it exits so that an exit is observable
// through kill(pid, 0) rather than leaving a zombie behind.
func startLedgerOrphan(t *testing.T, dir string) *ledgerOrphan {
	t.Helper()
	cmd := exec.Command("sleep", "120")
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	o := &ledgerOrphan{pid: cmd.Process.Pid, exited: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(o.exited) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-o.pid, syscall.SIGKILL)
		<-o.exited
	})
	return o
}

func (o *ledgerOrphan) alive() bool {
	select {
	case <-o.exited:
		return false
	default:
		return true
	}
}

// ledgerStopFixture is a deployed, elastic app whose generation ledger the
// test seeds with a row the manager does not own.
type ledgerStopFixture struct {
	srv     *Server
	store   *db.Store
	token   string
	app     *db.App
	serving *db.Deployment
}

func newLedgerStopFixture(t *testing.T, slug string) *ledgerStopFixture {
	t.Helper()
	srv, store, token, manager, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
	t.Cleanup(srv.Close)
	app := createGenerationTestApp(t, store, slug, 2, 16)
	if rec := deployIsolationManifest(t, srv, token, app.Slug, "per_session", "print('v1')"); rec.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", rec.Code, rec.Body.String())
	}
	serving, err := store.GetServingDeployment(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if serving.BundleDir == "" {
		t.Fatal("serving deployment has no bundle dir, so no worker identity can be proven")
	}
	manager.RegisterRuntime("host", process.NewNativeRuntime())
	return &ledgerStopFixture{srv: srv, store: store, token: token, app: app, serving: serving}
}

func (f *ledgerStopFixture) record(t *testing.T, pid int, startIdentity int64) {
	t.Helper()
	if err := f.store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{
		AppID: f.app.ID, DeploymentID: f.serving.ID, Index: 7, PID: &pid, Status: "running",
		Provider: "native", Tier: "host", WorkerID: strconv.Itoa(pid), ProcessStartIdentity: startIdentity,
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *ledgerStopFixture) stop(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/apps/"+f.app.Slug+"/stop", nil)
	req.Header.Set("Authorization", "Bearer "+f.token)
	rec := httptest.NewRecorder()
	f.srv.Router().ServeHTTP(rec, req)
	return rec
}

func (f *ledgerStopFixture) rows(t *testing.T) []*db.DeploymentReplica {
	t.Helper()
	rows, err := f.store.ListDeploymentReplicas(f.app.ID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// Stopping an app must stop a ledgered worker the manager does not own, not
// just forget it: once its row is gone, neither startup recovery nor the
// watcher can find the process again.
func TestStopApp_StopsALedgeredWorkerTheManagerDoesNotOwn(t *testing.T) {
	for _, withIdentity := range []bool{true, false} {
		name := "without start identity"
		if withIdentity {
			name = "with start identity"
		}
		t.Run(name, func(t *testing.T) {
			f := newLedgerStopFixture(t, "ledger-orphan")
			orphan := startLedgerOrphan(t, f.serving.BundleDir)
			var started int64
			if withIdentity {
				var err error
				if started, err = process.NativeProcessStartIdentity(orphan.pid); err != nil {
					t.Fatal(err)
				}
			}
			f.record(t, orphan.pid, started)
			if !orphan.alive() {
				t.Fatal("the orphan exited before the stop, so this test cannot show the stop ending it")
			}

			if rec := f.stop(t); rec.Code != http.StatusOK {
				t.Fatalf("stop: %d %s", rec.Code, rec.Body.String())
			}
			select {
			case <-orphan.exited:
			case <-time.After(30 * time.Second):
				t.Fatal("the stop returned but the ledgered worker is still running")
			}
			if rows := f.rows(t); len(rows) != 0 {
				t.Fatalf("ledger after a confirmed stop = %+v, want it cleared", rows)
			}
		})
	}
}

// A start identity that differs from the recorded one proves the PID was
// reused, even by a process that runs in the deployment's bundle (a
// maintenance command, say). The stop must not signal it, and the recorded
// worker is provably gone, so its row is cleared.
func TestStopApp_LeavesAReusedPIDInTheBundleAlone(t *testing.T) {
	f := newLedgerStopFixture(t, "ledger-reused")
	stranger := startLedgerOrphan(t, f.serving.BundleDir)
	started, err := process.NativeProcessStartIdentity(stranger.pid)
	if err != nil {
		t.Fatal(err)
	}
	f.record(t, stranger.pid, started+1)

	if rec := f.stop(t); rec.Code != http.StatusOK {
		t.Fatalf("stop: %d %s", rec.Code, rec.Body.String())
	}
	time.Sleep(200 * time.Millisecond)
	if !stranger.alive() {
		t.Fatal("the stop signalled a process whose start identity is not the recorded worker's")
	}
	if rows := f.rows(t); len(rows) != 0 {
		t.Fatalf("ledger after the stop = %+v, want the reused PID's row cleared", rows)
	}
}

// A ledgered PID that now runs outside the deployment's bundle is not provably
// the recorded worker: it may be the number reused by an unrelated process. The
// stop must neither signal it nor forget the row, and must say it could not
// confirm the stop.
func TestStopApp_KeepsTheRowOfAWorkerItCannotIdentify(t *testing.T) {
	f := newLedgerStopFixture(t, "ledger-unproven")
	stranger := startLedgerOrphan(t, t.TempDir())
	f.record(t, stranger.pid, 0)

	rec := f.stop(t)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stop over an unidentifiable live PID: %d %s, want it refused", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "confirmed stopped") {
		t.Fatalf("refusal body = %s, want it to say the stop could not be confirmed", rec.Body.String())
	}
	// The refusal itself is immediate, but a wrongly sent SIGTERM takes a
	// moment to land.
	time.Sleep(200 * time.Millisecond)
	if !stranger.alive() {
		t.Fatal("the stop signalled a process it could not prove was the recorded worker")
	}
	rows := f.rows(t)
	if len(rows) != 1 || rows[0].PID == nil || *rows[0].PID != stranger.pid {
		t.Fatalf("ledger after the refusal = %+v, want the row kept", rows)
	}
}

// A row whose process is gone, or whose PID now belongs to another user (which
// a worker never does: native workers run as the server's own user), is
// confirmed stopped. Neither may hold the app's stop back.
func TestStopApp_ClearsRowsWhoseWorkerIsProvablyGone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root every PID can be signalled, so no process is another user's")
	}
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	foreign := foreignProcessGroupLeader(t)
	for name, pid := range map[string]int{
		"exited":             dead.Process.Pid,
		"another user's pid": foreign,
	} {
		t.Run(name, func(t *testing.T) {
			f := newLedgerStopFixture(t, "ledger-gone")
			f.record(t, pid, 0)
			if rec := f.stop(t); rec.Code != http.StatusOK {
				t.Fatalf("stop: %d %s", rec.Code, rec.Body.String())
			}
			if rows := f.rows(t); len(rows) != 0 {
				t.Fatalf("ledger after the stop = %+v, want it cleared", rows)
			}
		})
	}
}

// foreignProcessGroupLeader returns a PID above 1 that this user may not
// signal, as a process or as a group: a live process of another user.
func foreignProcessGroupLeader(t *testing.T) int {
	t.Helper()
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

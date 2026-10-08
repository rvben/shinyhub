package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/fleet"
	"github.com/rvben/shinyhub/internal/jobs"
	"github.com/rvben/shinyhub/internal/lifecycle/scheduler"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// recoveryRuntime runs the real deployment and producer bookkeeping with
// synthetic process lifetimes, so the test needs no Python installation.
type recoveryRuntime struct {
	mu        sync.Mutex
	nextPID   int
	stops     map[int]chan struct{}
	producers []string
}

func (r *recoveryRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextPID++
	pid := r.nextPID
	r.stops[pid] = make(chan struct{})
	return process.ReplicaEndpoint{URL: "http://127.0.0.1:" + strconv.Itoa(p.Port), Provider: "native", WorkerID: strconv.Itoa(pid), Handle: process.RunHandle{PID: pid}}, nil
}

func (r *recoveryRuntime) Signal(h process.RunHandle, _ syscall.Signal) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if stop, ok := r.stops[h.PID]; ok {
		select {
		case <-stop:
		default:
			close(stop)
		}
	}
	return nil
}

func (r *recoveryRuntime) Wait(ctx context.Context, h process.RunHandle) error {
	r.mu.Lock()
	stop := r.stops[h.PID]
	r.mu.Unlock()
	select {
	case <-stop:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (*recoveryRuntime) Stats(context.Context, process.RunHandle) (*float64, uint64, error) {
	return nil, 0, nil
}

func (r *recoveryRuntime) RunOnce(_ context.Context, p process.StartParams, _ io.Writer) (process.ExitInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.producers = append(r.producers, p.ContentDigest)
	return process.ExitInfo{}, nil
}

func (*recoveryRuntime) HostPreparesDeps() bool      { return false }
func (*recoveryRuntime) HostProvidesAppData() bool   { return true }
func (*recoveryRuntime) InheritsLifetimeFiles() bool { return true }
func (*recoveryRuntime) AppBindHost() string         { return "127.0.0.1" }

func TestFleetApplyRealServerRecoversLastGoodBundle(t *testing.T) {
	store := dbtest.New(t)
	appsDir := t.TempDir()
	runtime := &recoveryRuntime{nextPID: 900000, stops: map[int]chan struct{}{}}
	mgr := process.NewManager(appsDir, runtime)
	t.Cleanup(func() { _ = mgr.StopAll() })
	cfg := &config.Config{Auth: config.AuthConfig{Secret: "test-secret"}, Storage: config.StorageConfig{AppsDir: appsDir, AppDataDir: t.TempDir(), VersionRetention: 5}}
	srv := api.New(cfg, store, mgr, proxy.New())
	if err := store.CreateUser(db.CreateUserParams{Username: "recovery-admin", PasswordHash: "unused", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	user, err := store.GetUserByUsername("recovery-admin")
	if err != nil {
		t.Fatal(err)
	}
	token, err := auth.IssueJWT(user.ID, user.Username, user.Role, cfg.Auth.Secret)
	if err != nil {
		t.Fatal(err)
	}
	jm, err := jobs.NewManager(mgr, nil, process.DefaultTier, store, nil, appsDir, cfg.Storage.AppDataDir)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetJobs(jm, scheduler.New(jm, store, time.UTC))
	t.Cleanup(func() { jm.Stop(context.Background()) })
	var failConsumer atomic.Bool
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		p.HealthCheck = func(string, time.Duration, http.RoundTripper) error { return nil }
		if failConsumer.Load() && !p.PrepareOnly {
			return nil, errors.New("injected consumer import failure")
		}
		return deploy.Run(p)
	})
	server := httptest.NewServer(srv.Router())
	t.Cleanup(server.Close)
	t.Setenv("SHINYHUB_HOST", server.URL)
	t.Setenv("SHINYHUB_TOKEN", token)
	t.Setenv("SHINYHUB_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
	configPathOverride = ""
	t.Cleanup(func() { configPathOverride = "" })
	source := t.TempDir()
	appDir := filepath.Join(source, "app")
	const good = "print('good')\n"
	mustWrite(t, filepath.Join(appDir, "app.py"), good)
	mustWrite(t, filepath.Join(appDir, "producer.py"), "print('producer')\n")
	mustWrite(t, filepath.Join(appDir, "shinyhub.toml"), "[[schedule]]\nname='refresh'\ncron='0 5 * * *'\ncmd='python producer.py'\ndeploy_trigger='bundle_change'\n")
	manifest := writeFleetManifest(t, source, "fleet_id='recovery'\n[[app]]\nslug='app'\nsource='./app'\nvisibility='private'\n")
	apply := func() applyJSONEnvelope {
		t.Helper()
		out, stderr, err := execCLISplit(t, "fleet", "apply", "-f", manifest, "--yes", "--allow-downtime", "--wait-for-warm", "--verify-health", "--json")
		if err != nil {
			if app, getErr := store.GetAppBySlug("app"); getErr == nil {
				schedules, _ := store.ListSchedulesByApp(app.ID)
				for _, schedule := range schedules {
					runs, _ := store.ListScheduleRuns(schedule.ID, 10, 0)
					t.Logf("schedule=%+v runs=%+v", schedule, runs)
				}
			}
			t.Fatalf("apply: %v\n%s\n%s", err, out, stderr)
		}
		var env applyJSONEnvelope
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("apply JSON: %v\n%s", err, out)
		}
		return env
	}
	first := apply()
	if len(first.Apps) != 1 || first.Apps[0].Result.Status != string(statusCreated) {
		t.Fatalf("first apply=%+v", first)
	}
	goodApp, err := store.GetAppBySlug("app")
	if err != nil {
		t.Fatal(err)
	}
	failConsumer.Store(true)
	mustWrite(t, filepath.Join(appDir, "app.py"), "print('bad dependency')\n")
	var progress bytes.Buffer
	_, _, _, _, err = deployAppBundleFromSpecWithDowntime(&cliConfig{Host: server.URL, Token: token}, "app", bundleBuildSpec{Dir: appDir}, "private", "", &progress, "", time.Second, true)
	if err == nil {
		t.Fatal("bad consumer unexpectedly deployed")
	}
	failed, err := store.GetAppBySlug("app")
	if err != nil || failed.LastDeploymentStatus != "failed" || failed.ContentDigest != goodApp.ContentDigest || failed.Status != "failed" {
		t.Fatalf("failed attempt did not reproduce report: app=%+v err=%v", failed, err)
	}
	if quarantined, err := store.AppDeploymentCompatibilityQuarantined(failed.ID); err != nil || !quarantined {
		t.Fatalf("missing deployment barrier=%v err=%v", quarantined, err)
	}
	if repair, err := store.AppDataCompatibilityQuarantined(failed.ID); err != nil || repair {
		t.Fatalf("producer should have succeeded: writer repair=%v err=%v", repair, err)
	}
	failConsumer.Store(false)
	mustWrite(t, filepath.Join(appDir, "app.py"), good)
	second := apply()
	row := second.Apps[0]
	if row.RecoveryReason != fleet.RecoveryFailed || row.Digest.Local != row.Digest.Server || row.Result.Status != string(statusUpdated) {
		t.Fatalf("matching good bundle was not repaired: %+v", row)
	}
	if quarantined, err := store.AppCompatibilityQuarantined(failed.ID); err != nil || quarantined {
		t.Fatalf("repair left quarantine=%v err=%v", quarantined, err)
	}
	runtime.mu.Lock()
	publications := append([]string(nil), runtime.producers...)
	runtime.mu.Unlock()
	if len(publications) != 3 || publications[0] != goodApp.ContentDigest || publications[1] == goodApp.ContentDigest || publications[2] != goodApp.ContentDigest {
		t.Fatalf("expected good/bad/good producer publications, got %v", publications)
	}
	third := apply()
	if third.Apps[0].Result.Status != string(statusUnchanged) || third.Apps[0].RecoveryReason != "" {
		t.Fatalf("post-repair apply=%+v", third.Apps[0])
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.producers) != 3 {
		t.Fatal("unchanged apply republished producers")
	}
}

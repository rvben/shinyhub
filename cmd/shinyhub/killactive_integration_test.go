//go:build integration

package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/fargate"
)

const authSecret = "killactive-e2e-secret-killactive-e2e-secret"

var testBin string // path to the built shinyhub binary; set by TestMain.

func TestMain(m *testing.M) {
	// Build only when Postgres is configured; otherwise the single test skips
	// and a needless compile is avoided.
	if os.Getenv("SHINYHUB_TEST_POSTGRES_DSN") == "" {
		os.Exit(m.Run())
	}
	root := repoRoot()
	bin := filepath.Join(os.TempDir(), fmt.Sprintf("shinyhub-killactive-%d", os.Getpid()))
	build := exec.Command("go", "build", "-o", bin, "./cmd/shinyhub")
	build.Dir = root
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build shinyhub: %v\n%s", err, out)
		os.Exit(1)
	}
	testBin = bin
	code := m.Run()
	_ = os.Remove(bin)
	os.Exit(code)
}

// repoRoot walks up from this test file to the module root (go.mod).
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("go.mod not found above " + file)
		}
		dir = parent
	}
}

// stub is an in-test HTTP backend standing in for one off-host fargate replica.
type stub struct {
	srv  *httptest.Server
	hits atomic.Int64
	body string
}

func newStub(t *testing.T, idx int) *stub {
	t.Helper()
	s := &stub{body: fmt.Sprintf("stub-%d", idx)}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(s.body))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// instance is a spawned `shinyhub serve` child process.
type instance struct {
	id     string
	port   int
	cmd    *exec.Cmd
	stderr *bytes.Buffer
	waited bool
}

func writeConfig(t *testing.T, path, id string, port int, dsn, tmp string) {
	t.Helper()
	cfg := fmt.Sprintf(`server:
  host: 127.0.0.1
  port: %d
  instance_id: %q
  lease_ttl: 5s
  lease_renew_every: 500ms
database:
  dsn: %q
storage:
  apps_dir: %q
  app_data_dir: %q
runtime:
  tiers:
    - name: fargate
      runtime: fargate
  fargate:
    cluster: shinyhub-test
    task_definition: shinyhub-td
    container_name: app
    subnets: [subnet-test]
    control_plane_url: http://127.0.0.1:%d
    task_cpu_units: 256
    task_memory_mb: 512
`, port, id, dsn, filepath.Join(tmp, "apps-"+id), filepath.Join(tmp, "data-"+id), port)
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config %s: %v", id, err)
	}
}

func startInstance(t *testing.T, id string, port int, dsn, tmp string) *instance {
	t.Helper()
	cfgPath := filepath.Join(tmp, "config-"+id+".yaml")
	writeConfig(t, cfgPath, id, port, dsn, tmp)

	var stderr bytes.Buffer
	cmd := exec.Command(testBin, "serve", "--config", cfgPath)
	cmd.Stderr = &stderr
	// Auth secret is shared so both instances derive the SAME sticky-cookie key
	// (cross-instance affinity). Dummy AWS env keeps the owner's ECS reconcile a
	// fast, hermetic no-op (connection-refused in ms, single attempt).
	cmd.Env = append(os.Environ(),
		"SHINYHUB_AUTH_SECRET="+authSecret,
		"AWS_ACCESS_KEY_ID=dummy",
		"AWS_SECRET_ACCESS_KEY=dummy",
		"AWS_REGION=us-east-1",
		"AWS_ENDPOINT_URL_ECS=http://127.0.0.1:1",
		"AWS_MAX_ATTEMPTS=1",
		"GOWORK=off",
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start instance %s: %v", id, err)
	}
	inst := &instance{id: id, port: port, cmd: cmd, stderr: &stderr}
	t.Cleanup(func() {
		inst.stop()
		if t.Failed() {
			t.Logf("instance %s stderr:\n%s", inst.id, inst.stderr.String())
		}
	})
	return inst
}

// wait reaps the process exactly once (Wait must not be called twice).
func (i *instance) wait() {
	if i.waited {
		return
	}
	i.waited = true
	_ = i.cmd.Wait()
}

func (i *instance) stop() {
	if i.cmd.Process != nil {
		_ = i.cmd.Process.Kill()
	}
	i.wait()
}

func (i *instance) url(path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", i.port, path)
}

// get issues a GET with a short timeout. A transport error (e.g. connection
// refused on a dead instance) returns code 0.
func get(t *testing.T, url string, cookie *http.Cookie) (int, []*http.Cookie, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request %s: %v", url, err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Cookies(), string(body)
}

// pollStatus polls url until it returns want, or fails after timeout.
func pollStatus(t *testing.T, url string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if code, _, _ := get(t, url, nil); code == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout: %s never returned %d within %s", url, want, timeout)
}

// seedRunningFargateApp seeds a public, running app with one running fargate
// replica per endpoint. WorkerID MUST be fargate.WorkerID: an empty worker id
// is treated as "declared gone" by RecoverProcesses and would mark the replica
// lost (recovery.go:301). App status MUST be 'running' so RecoverProcesses
// (ListRunningApps) actually iterates it.
func seedRunningFargateApp(t *testing.T, store *db.Store, slug string, endpoints []string) {
	t.Helper()
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "x", Role: "admin"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: owner.ID, Access: "public"}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "running"}); err != nil {
		t.Fatalf("set app running: %v", err)
	}
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	dep, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v1", BundleDir: filepath.Join(t.TempDir(), "bundle"),
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	for idx, ep := range endpoints {
		if err := store.UpsertReplica(db.UpsertReplicaParams{
			AppID: app.ID, Index: idx, Status: db.ReplicaStatusRunning,
			Provider: fargate.Provider, Tier: "fargate", WorkerID: fargate.WorkerID,
			EndpointURL: ep, DesiredState: "running", DeploymentID: &dep.ID,
		}); err != nil {
			t.Fatalf("upsert replica %d: %v", idx, err)
		}
	}
}

// stickyCookie returns the shinyhub_rep_<slug> cookie from a response's
// Set-Cookie list, or nil if none was set.
func stickyCookie(slug string, cookies []*http.Cookie) *http.Cookie {
	name := "shinyhub_rep_" + slug
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// stubFor maps a proxied response body back to its stub.
func stubFor(body string, stubs ...*stub) *stub {
	for _, s := range stubs {
		if s.body == body {
			return s
		}
	}
	return nil
}

func TestKillTheActive_StandbyTakesOver(t *testing.T) {
	store, dsn := dbtest.NewPostgres(t)
	tmp := t.TempDir()

	const slug = "demo-app"
	stub0 := newStub(t, 0)
	stub1 := newStub(t, 1)
	seedRunningFargateApp(t, store, slug, []string{stub0.srv.URL, stub1.srv.URL})

	// --- boot instance A and confirm it serves (/readyz=200) ---
	instA := startInstance(t, "a", 18090, dsn, tmp)
	pollStatus(t, instA.url("/readyz"), http.StatusOK, 30*time.Second)

	// A is the sole instance -> it wins the lease and becomes active.
	pollStatus(t, instA.url("/activez"), http.StatusOK, 15*time.Second)

	// --- boot standby B; it serves (/readyz) but is NOT active (A holds lease) ---
	instB := startInstance(t, "b", 18091, dsn, tmp)
	pollStatus(t, instB.url("/readyz"), http.StatusOK, 30*time.Second)

	if code, _, _ := get(t, instB.url("/activez"), nil); code != http.StatusServiceUnavailable {
		t.Fatalf("standby B /activez = %d, want 503 while A holds the lease", code)
	}
	// LB contract: both ready, exactly one active.
	if code, _, _ := get(t, instA.url("/readyz"), nil); code != http.StatusOK {
		t.Fatalf("A /readyz = %d, want 200", code)
	}

	// --- pre-crash: A serves /app and pins a replica via a sticky cookie ---
	codeA, setCookies, bodyA := get(t, instA.url("/app/"+slug+"/"), nil)
	if codeA != http.StatusOK {
		t.Fatalf("A /app pre-crash = %d, want 200 (body %q)", codeA, bodyA)
	}
	pinned := stubFor(bodyA, stub0, stub1)
	other := stub1
	if pinned == stub1 {
		other = stub0
	}
	if pinned == nil {
		t.Fatalf("A /app body %q matched neither stub", bodyA)
	}
	cookie := stickyCookie(slug, setCookies)
	if cookie == nil {
		t.Fatal("A did not set a sticky cookie - cannot assert cross-instance affinity")
	}

	// --- cross-instance affinity: B honors A's HMAC-signed cookie ---
	otherBefore := other.hits.Load()
	codeB, bReissue, bodyB := get(t, instB.url("/app/"+slug+"/"), cookie)
	if codeB != http.StatusOK {
		t.Fatalf("B /app with A's cookie = %d, want 200 (body %q)", codeB, bodyB)
	}
	if bodyB != pinned.body {
		t.Fatalf("B routed to %q, want pinned %q - cross-instance affinity broken", bodyB, pinned.body)
	}
	if stickyCookie(slug, bReissue) != nil {
		t.Fatal("B reissued a sticky cookie - it re-picked instead of honoring A's HMAC-signed cookie")
	}
	if other.hits.Load() != otherBefore {
		t.Fatal("the non-pinned stub was hit - B did not honor the pin")
	}
}

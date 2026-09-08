package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/process"
)

// newLimitEnforcementServer builds a server backed by a real process manager,
// which is what the PATCH handler asks whether per-app limits actually bind.
// A server with a nil manager cannot answer the question and is deliberately
// silent, so the manager is what makes this test capable of failing.
func newLimitEnforcementServer(t *testing.T) (*api.Server, *db.Store, *process.Manager) {
	t.Helper()
	store := dbtest.New(t)
	appsDir := t.TempDir()
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: appsDir, AppDataDir: t.TempDir()},
	}
	mgr := process.NewManager(appsDir, process.NewNativeRuntime())
	return api.New(cfg, store, mgr, nil), store, mgr
}

func seedLimitApp(t *testing.T, store *db.Store) (slug, token string) {
	t.Helper()
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "limituser", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("limituser")
	tok, _ := auth.IssueJWT(u.ID, "limituser", "admin", "test-secret")
	store.CreateApp(db.CreateAppParams{Slug: "limitapp", Name: "Limit App", OwnerID: u.ID})
	return "limitapp", tok
}

func patchLimitApp(t *testing.T, srv *api.Server, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := authedRequest(t, "PATCH", "/api/apps/limitapp", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

// TestPatchApp_LimitWarningTracksActualEnforcement verifies that setting a real
// memory limit warns exactly when the runtime cannot enforce it. The expected
// answer is read from the same manager the handler consults rather than
// hardcoded, because cgroup v2 delegation is a property of the host: on a
// delegated Linux service the limit binds and there is nothing to warn about,
// and on a host without cgroups (macOS, an undelegated container) it does not.
// What is pinned is the pairing, which is the wiring the handler must have.
func TestPatchApp_LimitWarningTracksActualEnforcement(t *testing.T) {
	srv, store, mgr := newLimitEnforcementServer(t)
	_, token := seedLimitApp(t, store)

	memEnforced, _ := mgr.ResourceEnforcement()

	rec := patchLimitApp(t, srv, token, []byte(`{"memory_limit_mb":512}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	warn := rec.Header().Get("X-ShinyHub-Warning")
	warned := strings.Contains(warn, "not enforced")

	if memEnforced && warned {
		t.Errorf("host enforces memory limits, so no unenforced warning is due, got %q", warn)
	}
	if !memEnforced && !warned {
		t.Errorf("host does not enforce memory limits, so setting one must warn; header was %q", warn)
	}
	if warned && !strings.Contains(warn, "memory limit") {
		t.Errorf("warning should name the memory limit, got %q", warn)
	}
}

// TestPatchApp_UnlimitedNeverWarns pins the other side, and does so on every
// host: memory_limit_mb=0 asks for no cap at all, so however this host handles
// cgroups there is no limit that could fail to be enforced. Without the
// effective-limit gate the handler would warn here too, telling an operator who
// deliberately removed a cap that their non-existent cap is inert.
func TestPatchApp_UnlimitedNeverWarns(t *testing.T) {
	srv, store, _ := newLimitEnforcementServer(t)
	_, token := seedLimitApp(t, store)

	rec := patchLimitApp(t, srv, token, []byte(`{"memory_limit_mb":0,"cpu_quota_percent":0}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if warn := rec.Header().Get("X-ShinyHub-Warning"); strings.Contains(warn, "not enforced") {
		t.Errorf("setting both limits to unlimited must not warn about enforcement, got %q", warn)
	}
}

// TestPatchApp_UntouchedLimitsNeverWarn pins that the advisory is attached to
// the act of setting a limit, not to the host. A PATCH that renames the app on
// an unenforcing host must stay silent, or every unrelated edit would carry a
// resource warning until the operator stopped reading them.
func TestPatchApp_UntouchedLimitsNeverWarn(t *testing.T) {
	srv, store, _ := newLimitEnforcementServer(t)
	_, token := seedLimitApp(t, store)

	rec := patchLimitApp(t, srv, token, []byte(`{"name":"Renamed"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if warn := rec.Header().Get("X-ShinyHub-Warning"); strings.Contains(warn, "not enforced") {
		t.Errorf("a PATCH that touches no resource limit must not warn about enforcement, got %q", warn)
	}
}

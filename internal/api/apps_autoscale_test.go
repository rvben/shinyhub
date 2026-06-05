package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// newAutoscaleTestServer builds a server with an explicit replica ceiling so the
// max-bound validation can be exercised.
func newAutoscaleTestServer(t *testing.T, maxReplicas int, defaultTarget float64) (*api.Server, *db.Store) {
	t.Helper()
	store := dbtest.New(t)
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
		Runtime: config.RuntimeConfig{
			MaxReplicas: maxReplicas,
			Autoscale:   config.AutoscaleConfig{DefaultTarget: defaultTarget},
		},
	}
	srv := api.New(cfg, store, nil, nil)
	return srv, store
}

func seedAutoscaleApp(t *testing.T, store *db.Store) (slug, token string) {
	t.Helper()
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("bob")
	tok, _ := auth.IssueJWT(u.ID, "bob", "admin", "test-secret")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})
	return "myapp", tok
}

func patchAutoscale(t *testing.T, srv *api.Server, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := authedRequest(t, "PATCH", "/api/apps/myapp", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func TestPatchApp_EnableAutoscalePersists(t *testing.T) {
	srv, store := newAutoscaleTestServer(t, 16, 0.8)
	_, token := seedAutoscaleApp(t, store)

	body := []byte(`{"autoscale":{"enabled":true,"min_replicas":2,"max_replicas":8,"target":0.75}}`)
	rec := patchAutoscale(t, srv, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	app, _ := store.GetAppBySlug("myapp")
	if !app.AutoscaleEnabled {
		t.Fatalf("AutoscaleEnabled not persisted")
	}
	if app.AutoscaleMinReplicas != 2 || app.AutoscaleMaxReplicas != 8 {
		t.Fatalf("bounds = %d/%d, want 2/8", app.AutoscaleMinReplicas, app.AutoscaleMaxReplicas)
	}
	if app.AutoscaleTarget != 0.75 {
		t.Fatalf("target = %v, want 0.75", app.AutoscaleTarget)
	}
}

func TestPatchApp_AutoscaleRejectsMinBelowOne(t *testing.T) {
	srv, store := newAutoscaleTestServer(t, 16, 0.8)
	_, token := seedAutoscaleApp(t, store)
	body := []byte(`{"autoscale":{"enabled":true,"min_replicas":0,"max_replicas":8}}`)
	if rec := patchAutoscale(t, srv, token, body); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for min<1, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPatchApp_AutoscaleRejectsMinAboveMax(t *testing.T) {
	srv, store := newAutoscaleTestServer(t, 16, 0.8)
	_, token := seedAutoscaleApp(t, store)
	body := []byte(`{"autoscale":{"enabled":true,"min_replicas":5,"max_replicas":2}}`)
	if rec := patchAutoscale(t, srv, token, body); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for min>max, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPatchApp_AutoscaleRejectsMaxAboveCeiling(t *testing.T) {
	srv, store := newAutoscaleTestServer(t, 4, 0.8)
	_, token := seedAutoscaleApp(t, store)
	body := []byte(`{"autoscale":{"enabled":true,"min_replicas":1,"max_replicas":10}}`)
	if rec := patchAutoscale(t, srv, token, body); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for max>ceiling, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPatchApp_AutoscaleRejectsTargetOutOfRange(t *testing.T) {
	srv, store := newAutoscaleTestServer(t, 16, 0.8)
	_, token := seedAutoscaleApp(t, store)
	body := []byte(`{"autoscale":{"enabled":true,"min_replicas":1,"max_replicas":4,"target":1.5}}`)
	if rec := patchAutoscale(t, srv, token, body); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for target>1, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPatchApp_AutoscaleRejectsNegativeMinWhenDisabled(t *testing.T) {
	// Bounds are persisted even while disabled (so a re-enable restores the
	// operator's last choice), so they must satisfy the stored DB range
	// regardless of the enabled flag. A negative min would otherwise pass this
	// handler and only fail the column CHECK, returning a 500 after sibling
	// fields in the same PATCH were already committed.
	srv, store := newAutoscaleTestServer(t, 16, 0.8)
	_, token := seedAutoscaleApp(t, store)
	body := []byte(`{"name":"Renamed","autoscale":{"min_replicas":-1}}`)
	rec := patchAutoscale(t, srv, token, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for negative min while disabled, got %d: %s", rec.Code, rec.Body.String())
	}
	// The bad autoscale field must abort the whole PATCH: the sibling name change
	// must not have been persisted before the validation failed.
	app, _ := store.GetAppBySlug("myapp")
	if app.Name != "My App" {
		t.Fatalf("sibling name change persisted despite invalid autoscale bounds: %q", app.Name)
	}
}

func TestPatchApp_AutoscaleRejectsBoundsAboveDBLimitWhenDisabled(t *testing.T) {
	// The stored columns cap at 1000; a value above that must be rejected with a
	// 400 here rather than failing the DB CHECK mid-update.
	srv, store := newAutoscaleTestServer(t, 16, 0.8)
	_, token := seedAutoscaleApp(t, store)
	body := []byte(`{"autoscale":{"max_replicas":1001}}`)
	if rec := patchAutoscale(t, srv, token, body); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for max above the stored limit while disabled, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPatchApp_DisableAutoscaleClearsEnabled(t *testing.T) {
	srv, store := newAutoscaleTestServer(t, 16, 0.8)
	_, token := seedAutoscaleApp(t, store)

	enable := []byte(`{"autoscale":{"enabled":true,"min_replicas":2,"max_replicas":8,"target":0.5}}`)
	if rec := patchAutoscale(t, srv, token, enable); rec.Code != http.StatusOK {
		t.Fatalf("enable: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	disable := []byte(`{"autoscale":{"enabled":false}}`)
	if rec := patchAutoscale(t, srv, token, disable); rec.Code != http.StatusOK {
		t.Fatalf("disable: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	app, _ := store.GetAppBySlug("myapp")
	if app.AutoscaleEnabled {
		t.Fatalf("AutoscaleEnabled = true after disable, want false")
	}
}

func TestGetApp_SurfacesEffectiveAutoscaleTarget(t *testing.T) {
	srv, store := newAutoscaleTestServer(t, 16, 0.8)
	_, token := seedAutoscaleApp(t, store)

	// App target 0 means inherit the runtime default (0.8).
	req := authedRequest(t, "GET", "/api/apps/myapp", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, ok := resp["effective_autoscale_target"]
	if !ok {
		t.Fatalf("envelope missing effective_autoscale_target: %v", resp)
	}
	if got != 0.8 {
		t.Fatalf("effective_autoscale_target = %v, want 0.8 (inherited)", got)
	}
}

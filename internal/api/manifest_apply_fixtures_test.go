package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/lifecycle/scheduler"
)

func ptrIntAPI(v int) *int { return &v }

// newServerWithOwnedApp returns a Server, Store, and the ownerID for an app
// the owner can manage. The scheduler is wired but not started.
func newServerWithOwnedApp(t *testing.T, slug string) (*Server, *db.Store, int64) {
	t.Helper()
	return newServerWithOwnedAppCfg(t, slug, manifestServerCfg{})
}

func newServerWithOwnedAppAndMaxReplicas(t *testing.T, slug string, max int) (*Server, *db.Store, int64) {
	t.Helper()
	return newServerWithOwnedAppCfg(t, slug, manifestServerCfg{MaxReplicas: max})
}

func newServerWithOwnedApp_NoScheduler(t *testing.T, slug string) (*Server, *db.Store, int64) {
	t.Helper()
	return newServerWithOwnedAppCfg(t, slug, manifestServerCfg{NoScheduler: true})
}

type manifestServerCfg struct {
	MaxReplicas int
	NoScheduler bool
}

func newServerWithOwnedAppCfg(t *testing.T, slug string, cfg manifestServerCfg) (*Server, *db.Store, int64) {
	t.Helper()

	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	// Create the owner user.
	hash, _ := auth.HashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{
		Username: "owner", PasswordHash: hash, Role: "developer",
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}

	// Create the app.
	if err := store.CreateApp(db.CreateAppParams{
		Slug: slug, Name: slug, OwnerID: owner.ID,
	}); err != nil {
		t.Fatal(err)
	}

	appsDir := t.TempDir()
	serverCfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: appsDir},
		Runtime: config.RuntimeConfig{MaxReplicas: cfg.MaxReplicas},
	}

	srv := New(serverCfg, store, nil, nil)

	if !cfg.NoScheduler {
		// Wire a scheduler that is instantiated but not started, so Reload
		// returns ErrNotStarted (soft failure path).
		sc := scheduler.New(nil, store)
		srv.SetJobs(nil, sc)
	}

	return srv, store, owner.ID
}

// newAuthedManifestRequest returns a request with the given user's identity
// injected into the context — matching the pattern used by requireManageApp.
func newAuthedManifestRequest(t *testing.T, userID int64, method, path string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r = r.WithContext(auth.WithUser(r.Context(), &auth.ContextUser{
		ID:       userID,
		Username: "owner",
		Role:     "developer",
	}))
	return r
}

// auditEventsContain reports whether any event in the slice matches the given
// (action, resourceID) pair.
func auditEventsContain(events []db.AuditEvent, action, resourceID string) bool {
	for _, e := range events {
		if e.Action == action && e.ResourceID == resourceID {
			return true
		}
	}
	return false
}

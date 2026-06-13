package api_test

import (
	"net/http"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestGrantAppAccess_AdvertisesVisibilityHeader verifies a grant advertises the
// app's visibility so the CLI can warn when a grant has no effect (the app is
// still private and members cannot reach it).
func TestGrantAppAccess_AdvertisesVisibilityHeader(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, ownerTok := mkUser(t, store, "owner", "developer")
	mkUser(t, store, "alice", "developer")
	if err := store.CreateApp(db.CreateAppParams{Slug: "app", Name: "App", OwnerID: ownerID}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAppAccess("app", "private"); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, "POST", "/api/apps/app/members", ownerTok, []byte(`{"username":"alice"}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("grant = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Shinyhub-App-Access"); got != "private" {
		t.Errorf("X-Shinyhub-App-Access = %q, want \"private\"", got)
	}

	if err := store.SetAppAccess("app", "shared"); err != nil {
		t.Fatal(err)
	}
	rec = do(t, srv, "POST", "/api/apps/app/members", ownerTok, []byte(`{"username":"alice"}`))
	if got := rec.Header().Get("X-Shinyhub-App-Access"); got != "shared" {
		t.Errorf("X-Shinyhub-App-Access = %q, want \"shared\"", got)
	}
}

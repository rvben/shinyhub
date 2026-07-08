package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestGrantAppAccess_WarnsWhenVisibilityAlreadyOpen verifies a member grant on
// a shared or public app advertises, via X-ShinyHub-Warning (the same channel
// group grants use), that everyone can already view the app, and that a grant
// on a private app - the case where membership is what admits the user - stays
// silent. The legacy X-Shinyhub-App-Access header, which existed only to drive
// a CLI note that wrongly claimed grants need shared visibility, must be gone.
func TestGrantAppAccess_WarnsWhenVisibilityAlreadyOpen(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, ownerTok := mkUser(t, store, "owner", "developer")
	mkUser(t, store, "alice", "developer")
	if err := store.CreateApp(db.CreateAppParams{Slug: "app", Name: "App", OwnerID: ownerID}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		access   string
		wantWarn string // required substring; empty = no warning at all
	}{
		{"private", ""},
		{"shared", "shared"},
		{"public", "public"},
	}
	for _, tc := range cases {
		if err := store.SetAppAccess("app", tc.access); err != nil {
			t.Fatal(err)
		}
		rec := do(t, srv, "POST", "/api/apps/app/members", ownerTok, []byte(`{"username":"alice"}`))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s: grant = %d, want 204; body=%s", tc.access, rec.Code, rec.Body.String())
		}
		warn := rec.Header().Get("X-ShinyHub-Warning")
		if tc.wantWarn == "" {
			if warn != "" {
				t.Errorf("%s: unexpected warning %q", tc.access, warn)
			}
		} else if !strings.Contains(warn, tc.wantWarn) {
			t.Errorf("%s: warning = %q, want it to mention %q", tc.access, warn, tc.wantWarn)
		}
		if got := rec.Header().Get("X-Shinyhub-App-Access"); got != "" {
			t.Errorf("%s: X-Shinyhub-App-Access should no longer be sent, got %q", tc.access, got)
		}
	}
}

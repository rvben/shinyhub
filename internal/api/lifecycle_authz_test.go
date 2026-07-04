package api_test

import (
	"net/http"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestMutatingLifecycleHandlers_RejectNonManager proves each mutating
// app-lifecycle handler enforces its own manage-access check: an unrelated
// authenticated user (not owner/admin/operator/manager-member) is refused on
// every one of them. Previously only the owner/admin path was exercised, so a
// handler-local guard dropped in a refactor of any single endpoint would not be
// caught (TEST-6). requireManageApp returns 404 (not 403) by the codebase's
// deliberate existence-oracle-avoidance convention.
func TestMutatingLifecycleHandlers_RejectNonManager(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, _ := mkUser(t, store, "owner", "developer")
	_, intruderTok := mkUser(t, store, "intruder", "developer")
	if err := store.CreateApp(db.CreateAppParams{Slug: "app", Name: "App", OwnerID: ownerID}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, method, path string
		body               []byte
	}{
		{"patch", "PATCH", "/api/apps/app", []byte(`{"name":"x"}`)},
		{"delete", "DELETE", "/api/apps/app", nil},
		{"deploy", "POST", "/api/apps/app/deploy", nil},
		{"rollback-post", "POST", "/api/apps/app/rollback", []byte(`{"deployment_id":1}`)},
		{"rollback-put", "PUT", "/api/apps/app/rollback", []byte(`{"deployment_id":1}`)},
		{"restart", "POST", "/api/apps/app/restart", nil},
		{"stop", "POST", "/api/apps/app/stop", nil},
		{"set-access", "PATCH", "/api/apps/app/access", []byte(`{"access":"public"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, srv, tc.method, tc.path, intruderTok, tc.body)
			if rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
				t.Errorf("%s %s as non-manager = %d, want 404/403 (manage guard); body=%s",
					tc.method, tc.path, rec.Code, rec.Body.String())
			}
		})
	}
}

package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// A mutation that answers with the app row must describe the app the same way
// the list payload describes it. The row comes straight out of the database,
// where every computed field sits at its zero value, and none of those fields
// is omitempty: an undecorated response asserts that the app has no session
// ceiling, no effective hibernate timeout and no manager, rather than saying
// nothing about them. A client merging that row onto its model - the whole
// reason the endpoint answers with a row - then hides every management control
// on an app the caller has just successfully managed.
// Fields whose value must not change just because the caller reached the app
// through a mutation instead of the list. status is deliberately absent: the
// mutation is what changes it.
var derivedAppFields = []string{
	"can_manage",
	"effective_hibernate_timeout_minutes",
	"effective_max_sessions_per_replica",
	"effective_worker_isolation",
	"sessions_ceiling",
}

func TestAppMutationResponseMatchesListRow(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"sleep", http.MethodPost, "/api/apps/derived/sleep", ""},
		{"stop", http.MethodPost, "/api/apps/derived/stop", ""},
		{"set access", http.MethodPatch, "/api/apps/derived/access", `{"access":"private"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, store := newTestServer(t)
			hash, _ := testHashPassword("pass")
			store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
			u, _ := store.GetUserByUsername("owner")
			store.CreateApp(db.CreateAppParams{Slug: "derived", Name: "Derived", OwnerID: u.ID})
			store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "derived", Status: "running"})
			srv.SetSleepOp(func(slug string) error {
				return store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "hibernated"})
			})
			token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")

			want := appListRow(t, srv, token, "derived")
			// The list row is only a useful reference if it carries the fields at
			// all: a positive control for the comparison below.
			if want["can_manage"] != true {
				t.Fatalf("list row reports can_manage=%v for the app's owner; the comparison below would prove nothing", want["can_manage"])
			}

			var body []byte
			if tc.body != "" {
				body = []byte(tc.body)
			}
			req := authedRequest(t, tc.method, tc.path, body, token)
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s %s = %d: %s", tc.method, tc.path, rec.Code, rec.Body.String())
			}
			var got map[string]any
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			for _, field := range derivedAppFields {
				if got[field] != want[field] {
					t.Errorf("%s response %s = %v, list row = %v", tc.name, field, got[field], want[field])
				}
			}
		})
	}
}

// Restart gets its own check: it is the response the dashboard merges onto a
// card after a card-local restart, and it is built from a different read than
// the other lifecycle handlers.
func TestRestartResponseMatchesListRow(t *testing.T) {
	h := newActivationHarness(t, "restarted")

	want := appListRow(t, h.srv, h.token, "restarted")
	if want["can_manage"] != true {
		t.Fatalf("list row reports can_manage=%v for an admin; the comparison below would prove nothing", want["can_manage"])
	}

	rec := h.post("/api/apps/restarted/restart")
	if rec.Code != http.StatusOK {
		t.Fatalf("restart = %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode restart response: %v", err)
	}
	for _, field := range derivedAppFields {
		if got[field] != want[field] {
			t.Errorf("restart response %s = %v, list row = %v", field, got[field], want[field])
		}
	}
}

// appListRow returns one app's row from GET /api/apps, which is the payload the
// dashboard builds its cards from.
func appListRow(t *testing.T, srv *api.Server, token, slug string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, http.MethodGet, "/api/apps", nil, token))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/apps = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode app list: %v", err)
	}
	for _, item := range payload.Items {
		if item["slug"] == slug {
			return item
		}
	}
	t.Fatalf("app %q not in list payload", slug)
	return nil
}

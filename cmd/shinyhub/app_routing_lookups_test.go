package main

import (
	"errors"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// fakeRoutes answers routing lookups from a fixed table and counts calls.
type fakeRoutes struct {
	apps  map[string]db.AppRouting
	err   error
	calls int
}

func (f *fakeRoutes) GetAppRoutingBySlug(slug string) (*db.AppRouting, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	a, ok := f.apps[slug]
	if !ok {
		return nil, db.ErrNotFound
	}
	return &a, nil
}

func TestAppRoutingLookupsAnswerFromTheRoutingRow(t *testing.T) {
	routes := &fakeRoutes{apps: map[string]db.AppRouting{
		"sales": {Name: "Sales dashboard", Access: "shared", Status: "crashed", LastError: "exit status 1"},
	}}
	l := appRoutingLookups(routes, func(string) bool { return false })

	if got := l.access("sales"); got != "shared" {
		t.Errorf("access = %q, want shared", got)
	}
	if got := l.name("sales"); got != "Sales dashboard" {
		t.Errorf("name = %q, want Sales dashboard", got)
	}
	if status, reason := l.status("sales"); status != "crashed" || reason != "exit status 1" {
		t.Errorf("status = (%q, %q), want (crashed, exit status 1)", status, reason)
	}
	if routes.calls != 3 {
		t.Errorf("routing lookups = %d, want one per answer", routes.calls)
	}
}

// A pending deployment row next to a stopped app shows the deploying page only
// while that deployment is really in flight; otherwise the stale row would hide
// the stopped page forever.
func TestAppRoutingStatusAsksWhetherTheDeployIsInFlight(t *testing.T) {
	routes := &fakeRoutes{apps: map[string]db.AppRouting{
		"sales": {Name: "Sales", Access: "private", Status: "stopped", LastDeploymentStatus: db.DeploymentPending},
	}}
	inFlight := map[string]bool{}
	l := appRoutingLookups(routes, func(slug string) bool { return inFlight[slug] })

	if status, _ := l.status("sales"); status != "stopped" {
		t.Fatalf("stale pending row: status = %q, want stopped", status)
	}
	inFlight["sales"] = true
	if status, _ := l.status("sales"); status != "deploying" {
		t.Fatalf("deploy in flight: status = %q, want deploying", status)
	}
}

// An app that cannot be read answers empty, which the proxy treats as unknown
// rather than as a real name, access mode or status.
func TestAppRoutingLookupsAnswerEmptyWhenTheAppCannotBeRead(t *testing.T) {
	for name, routes := range map[string]*fakeRoutes{
		"missing":        {apps: map[string]db.AppRouting{}},
		"database error": {err: errors.New("database is locked")},
	} {
		t.Run(name, func(t *testing.T) {
			l := appRoutingLookups(routes, func(string) bool { return true })
			if got := l.access("sales"); got != "" {
				t.Errorf("access = %q, want empty", got)
			}
			if got := l.name("sales"); got != "" {
				t.Errorf("name = %q, want empty", got)
			}
			if status, reason := l.status("sales"); status != "" || reason != "" {
				t.Errorf("status = (%q, %q), want empty", status, reason)
			}
		})
	}
}

// The proxy reads each answer through the routing lookups, never the full app
// lookup whose cost grows with deployment history. Their effect sits behind
// unexported proxy state, so pin each installation by source search.
func TestProxyAdmissionInstallsTheRoutingLookups(t *testing.T) {
	for _, needle := range []string{
		"appRoutingLookups(store, srv.DeployInFlight)",
		"prx.SetAppAccessLookup(routing.access)",
		"prx.SetAppStatusLookup(routing.status)",
		"prx.SetAppNameLookup(routing.name)",
	} {
		if n := sourceCount(t, "proxy_admission.go", needle); n != 1 {
			t.Errorf("proxy_admission.go contains %q %d times, want exactly once", needle, n)
		}
	}
}

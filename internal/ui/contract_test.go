package ui_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/ui"
)

// TestAppDetailUnwrapsGetAppResponse guards the API/frontend contract for
// GET /api/apps/:slug. The server returns a wrapped object
// (map[string]any{"app": app, "replicas_status": replicas}; see
// internal/api/apps.go handleGetApp) and the app-detail view must unwrap
// body.app before reading fields like slug or name.
//
// When the wrap was introduced, the frontend kept doing `const app = await
// resp.json()`, which made every field undefined and silently broke Save
// buttons on the detail page. This test ensures app-detail.js keeps reading
// from body.app so the class of regression can't recur.
func TestAppDetailUnwrapsGetAppResponse(t *testing.T) {
	assertContains(t, "views/app-detail.js", "body.app",
		"GET /api/apps/:slug returns {app, replicas_status}; see internal/api/apps.go handleGetApp")
	assertContains(t, "views/app-detail.js", "body.replicas_status",
		"GET /api/apps/:slug returns {app, replicas_status}; the Overview Replicas panel seeds from replicas_status")
}

// TestEnvListUnwrapsResponse guards the env-list consumer.
// GET /api/apps/:slug/env returns {env: [...]} (internal/api/env.go
// handleEnvList) and refreshEnvList in app.js reads data.env.
func TestEnvListUnwrapsResponse(t *testing.T) {
	assertContains(t, "app.js", "data.env",
		"GET /api/apps/:slug/env returns {env: [...]}; see internal/api/env.go handleEnvList")
}

// TestDataTabUnwrapsResponse guards the data-tab consumer.
// GET /api/apps/:slug/data returns {files, quota_mb, used_bytes}
// (internal/api/data.go handleDataList) and refreshDataTab in app.js
// reads env.files.
func TestDataTabUnwrapsResponse(t *testing.T) {
	assertContains(t, "app.js", "env.files",
		"GET /api/apps/:slug/data returns {files, quota_mb, used_bytes}; see internal/api/data.go handleDataList")
}

// TestAuditUnwrapsEnvelope guards the audit-log consumer.
// GET /api/audit returns {events, total, has_more} (internal/api/audit.go
// handleListAuditEvents). The UI's loadAuditEvents must read body.has_more
// to enable/disable the Next button — the previous heuristic of "fetch 101
// rows and check length > 100" disabled Next even when more pages existed.
func TestAuditUnwrapsEnvelope(t *testing.T) {
	assertContains(t, "app.js", "body.has_more",
		"GET /api/audit returns {events, total, has_more}; see internal/api/audit.go handleListAuditEvents")
	assertContains(t, "app.js", "body.events",
		"GET /api/audit returns {events, total, has_more}; consumer must read body.events")
}

// TestAppDetailPreservesOverviewURL guards against silent URL rewrites.
// /apps/<slug>/overview is a legitimate explicit-tab URL — it must not be
// replaced with /apps/<slug>. The presence of the canonicalising
// `history.replaceState` in mountAppDetail was the bug; this test fails
// if it comes back.
func TestAppDetailPreservesOverviewURL(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "views/app-detail.js")
	if err != nil {
		t.Fatalf("read app-detail.js: %v", err)
	}
	if strings.Contains(string(b), "history.replaceState({}, '', `/apps/${slug}`)") {
		t.Fatal("app-detail.js must not silently rewrite /apps/<slug>/overview to /apps/<slug>; preserve the user's URL")
	}
}

func assertContains(t *testing.T, path, needle, contract string) {
	t.Helper()
	b, err := fs.ReadFile(ui.Static(), path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(b), needle) {
		t.Fatalf("%s must contain %q to honor contract: %s", path, needle, contract)
	}
}

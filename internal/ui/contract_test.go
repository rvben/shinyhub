package ui_test

import (
	"io/fs"
	"strings"
	"testing"

	slugpkg "github.com/rvben/shinyhub/internal/slug"
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

// TestDeployHashHandlerWaitsForApps guards Codex review #1: handleDeployHash
// must wait for state.apps to populate before looking up the slug. Without
// this guard the post-login redirect from /#deploy=<slug> drops the slug
// before the matching app exists in memory, and the deploy modal never opens.
//
// We assert: (a) handleDeployHash is async, (b) the route mount in
// views/apps-grid.js awaits the initial /api/apps load before resolving so
// `await router.start()` actually waits for state.apps, (c) BOTH the
// bootstrap path (initialize) and the interactive login submit handler
// await handleDeployHash() — codex review found the second was missing,
// which silently broke the logged-out → /#deploy=<slug> → log-in → modal
// flow.
func TestDeployHashHandlerWaitsForApps(t *testing.T) {
	assertContains(t, "app.js", "async function handleDeployHash",
		"handleDeployHash must be async so it can wait for state.apps before consuming the slug")
	assertContains(t, "views/apps-grid.js", "export async function mountAppsGrid",
		"mountAppsGrid must be async and await its initial load so router.start() waits for state.apps")

	// Both the bootstrap and the interactive login paths must consume the
	// pending deploy hash. Counting occurrences guards against either path
	// silently dropping the call.
	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	got := strings.Count(string(b), "await handleDeployHash()")
	if got < 2 {
		t.Fatalf("app.js: `await handleDeployHash()` appears %d time(s); want at least 2 (bootstrap path in initialize() AND interactive login submit handler). A logged-out user landing on /#deploy=<slug> persists the slug; if the login path doesn't consume it, the deploy modal never opens after login.", got)
	}
}

// TestAccessVisibilityToggleSerialized guards Codex review #3: the
// access-visibility radio handler must serialize overlapping toggles so a
// rapid sequence of clicks cannot leave the UI desynced from the server.
// We assert the two pieces of the fix are present: a generation counter and
// a disabled-state writer that freezes the radio group during PATCH.
func TestAccessVisibilityToggleSerialized(t *testing.T) {
	assertContains(t, "app.js", "accessGen",
		"the access-visibility handler must use a generation counter to discard stale responses")
	assertContains(t, "app.js", "setAccessRadiosDisabled(true)",
		"the access-visibility handler must disable the radio group while a PATCH is in flight")
}

// TestSlugPatternStaysInSyncWithGoValidator guards against the SPA and the
// Go slug validator drifting apart. The regex literal in app.js and the
// `pattern=` attribute in index.html must both encode the canonical rule
// owned by internal/slug.
func TestSlugPatternStaysInSyncWithGoValidator(t *testing.T) {
	jsRegex := "/^" + slugpkg.Pattern + "$/"
	assertContains(t, "app.js", jsRegex,
		"SPA SLUG_RE must match internal/slug.Pattern; update both when changing the rule")
	htmlPattern := `pattern="` + slugpkg.Pattern + `"`
	assertContains(t, "index.html", htmlPattern,
		"new-app-slug input pattern attribute must match internal/slug.Pattern")
}

// TestSPAConsumesNextQueryParam guards the access-denied → log-in →
// original-app round trip. internal/access/middleware.go renderAccessDeniedPage
// builds /?next=<original> when an unauthenticated browser hits a private app;
// the SPA used to ignore the parameter and dump every user on /. Both the
// bootstrap (initialize) path and the interactive login submit handler must
// call consumeNextParam after router.start() so the user lands on the page
// they originally requested.
func TestSPAConsumesNextQueryParam(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "function consumeNextParam(") {
		t.Fatal("app.js: must define consumeNextParam(); see internal/access/middleware.go renderAccessDeniedPage which advertises /?next=<original>")
	}
	got := strings.Count(src, "consumeNextParam()")
	// One definition site is matched by `consumeNextParam(` above; here we
	// count the ()-suffixed call form to require >=2 invocations (bootstrap
	// + interactive-login).
	if got < 2 {
		t.Fatalf("app.js: consumeNextParam() called %d time(s); want at least 2 (bootstrap path AND interactive login submit handler) so a logged-out user reaching /?next=/apps/foo gets returned to /apps/foo after logging in", got)
	}
	if !strings.Contains(src, "internal/access/middleware.go") {
		// The fix references the access middleware in a code comment so a
		// reader can find the producer of the next= parameter. Asserting on
		// the comment keeps the breadcrumb intact.
		t.Fatal("app.js: consumeNextParam should reference internal/access/middleware.go in a comment so future readers can find the producer of the next= parameter")
	}
}

// TestRollbackHandlerBoundOnce guards against the duplicate-handler bug in
// renderDeployments. The earlier code called list.addEventListener('click',
// ...) inside load(), so every Retry attached another delegate and a single
// Roll back click fanned out into N concurrent POST /rollback requests
// (creating duplicate rollback deployments). Using `list.onclick = ...`
// outside load() makes the single-handler invariant structural — any
// re-binding replaces the previous handler instead of stacking.
func TestRollbackHandlerBoundOnce(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "views/app-detail.js")
	if err != nil {
		t.Fatalf("read app-detail.js: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "list.onclick =") {
		t.Fatal("app-detail.js: rollback delegate must be attached as `list.onclick = ...` so re-renders replace rather than stack the handler")
	}
	if strings.Contains(src, "list.addEventListener('click'") {
		t.Fatal("app-detail.js: must not use list.addEventListener('click', ...) for the rollback delegate; that stacks listeners across Retry clicks")
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

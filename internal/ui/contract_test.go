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

// TestSPASlugifyTruncatesBeforeTrim guards parity with the CLI's
// sanitizeSlug: the order MUST be slice(0,63) → trim trailing dashes, not
// trim → slice. With trim-then-slice an input long enough to land on `-`
// at byte 63 produces a slug ending in `-`, which SLUG_RE rejects. The
// fix on the CLI side (TestSanitizeSlug_TruncationProducesValidSlug) is
// useless if the SPA derivation drifts.
//
// We assert the structure of the slugify chain in app.js by requiring
// slice(0, 63) appears *before* the trailing-dash trim regex. We also
// simulate the chain in Go on a known-pathological input and assert the
// result satisfies slugpkg.Valid.
func TestSPASlugifyTruncatesBeforeTrim(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	src := string(b)
	// Find the slugify function body. We don't parse JS — we look for the
	// two ordered tokens `slice(0, 63)` and `replace(/^-+|-+$/g, '')` and
	// require the slice to come first.
	sliceIdx := strings.Index(src, ".slice(0, 63)")
	trimIdx := strings.Index(src, ".replace(/^-+|-+$/g, '')")
	if sliceIdx < 0 || trimIdx < 0 {
		t.Fatalf("app.js slugify: cannot locate .slice(0, 63) (%d) or trailing-dash trim (%d); both must be present", sliceIdx, trimIdx)
	}
	if sliceIdx > trimIdx {
		t.Fatal("app.js slugify: .slice(0, 63) appears AFTER the trailing-dash trim. The order MUST be slice → trim, otherwise long names produce slugs ending in `-` (which SLUG_RE rejects). See internal/cli/deploy.go sanitizeSlug for the canonical order.")
	}

	// Behavioral check on a Go-side simulation: emulate the chain on the
	// pathological input and assert the result is valid. We can't run JS
	// in a Go test, so we approximate: lowercase + ASCII-only input passes
	// through normalize/diacritic strip unchanged, so the only differences
	// from the JS chain are the regex engines, which agree on this input.
	in := strings.Repeat("a", 62) + "-bcdef"
	got := goEmulateSlugify(in)
	if len(got) > slugpkg.MaxLen {
		t.Errorf("emulated slugify(%q): len=%d > %d", in, len(got), slugpkg.MaxLen)
	}
	if !slugpkg.Valid(got) {
		t.Errorf("emulated slugify(%q) = %q; slugpkg.Valid rejects it. The SPA slugify must agree with the canonical rule.", in, got)
	}
}

// goEmulateSlugify mirrors app.js slugify() for ASCII inputs so the contract
// test can assert behavior without a JS runtime. Diacritic stripping is a
// no-op for ASCII so we only need lower → non-alphanum→`-` → slice(0,63) →
// trim leading/trailing dashes.
func goEmulateSlugify(in string) string {
	s := strings.ToLower(in)
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		alnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if alnum {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := b.String()
	if len(out) > slugpkg.MaxLen {
		out = out[:slugpkg.MaxLen]
	}
	out = strings.Trim(out, "-")
	return out
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

// TestRouterStartIsIdempotent guards against listener-stacking on the
// bootstrap → logout → login cycle. router.start() is called from both
// the initialize() bootstrap and the interactive login submit handler;
// without an idempotency guard the document accumulates duplicate click
// and popstate listeners on every login, causing a single SPA navigation
// to push duplicate history entries and mount the same view twice.
//
// We assert the router source declares a `started` flag and gates the
// listener attachment on it.
func TestRouterStartIsIdempotent(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "router.js")
	if err != nil {
		t.Fatalf("read router.js: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "let started = false") {
		t.Fatal("router.js must declare `let started = false` so start() is idempotent across login → logout → login")
	}
	if !strings.Contains(src, "if (!started) {") {
		t.Fatal("router.js start() must gate listener attachment with `if (!started)` to avoid stacking handlers on repeat invocations")
	}
}

// TestSPAConsumesNextQueryParam guards the access-denied → log-in →
// original-app round trip. internal/access/middleware.go renderAccessDeniedPage
// builds /?next=<RequestURI> when an unauthenticated browser hits a private
// app at /app/<slug>/...; the SPA used to ignore the parameter and dump every
// user on /. Both the bootstrap (initialize) path and the interactive login
// submit handler must call consumeNextParam after router.start() so the user
// lands on the page they originally requested.
//
// Critically: the producer's path is /app/<slug>/... (proxy-served, NOT a
// SPA route). consumeNextParam MUST hard-navigate (window.location.replace)
// for paths outside the SPA route allow-list — handing /app/... to
// router.navigate falls through to the no-match branch and lands the user
// on / again, which silently regresses the entire fix.
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
		t.Fatalf("app.js: consumeNextParam() called %d time(s); want at least 2 (bootstrap path AND interactive login submit handler) so a logged-out user reaching /?next=/app/foo/ gets returned to /app/foo/ after logging in", got)
	}
	if !strings.Contains(src, "internal/access/middleware.go") {
		t.Fatal("app.js: consumeNextParam should reference internal/access/middleware.go in a comment so future readers can find the producer of the next= parameter")
	}
	// The proxy path /app/<slug>/ is NOT a SPA route. consumeNextParam must
	// hard-navigate it via window.location.replace; router.navigate would
	// land on / instead.
	if !strings.Contains(src, "window.location.replace(raw)") {
		t.Fatal("app.js: consumeNextParam must use window.location.replace(raw) for non-SPA paths — the access-denied next= value is /app/<slug>/..., which the SPA router cannot mount. Without a hard navigation the user is dumped on / after login.")
	}
	// A SPA-route allow-list must exist so /apps/<slug> still goes through
	// the router (avoiding a full reload for an in-SPA target).
	if !strings.Contains(src, "SPA_ROUTE_PREFIXES") {
		t.Fatal("app.js: consumeNextParam must consult a SPA route allow-list (SPA_ROUTE_PREFIXES) so SPA paths take router.navigate while non-SPA paths take window.location.replace")
	}
}

// TestRollbackHandlerBoundOnce guards against the duplicate-handler bug in
// renderDeployments. The earlier code called list.addEventListener('click',
// ...) inside load(), so every Retry attached another delegate and a single
// Roll back click fanned out into N concurrent POST /rollback requests
// (creating duplicate rollback deployments). Using `list.onclick = ...`
// outside load() makes the single-handler invariant structural — any
// re-binding replaces the previous handler instead of stacking.
//
// We also pin the transport-failure recovery: the click handler MUST wrap
// the POST in try/catch and re-enable the button on any non-success path.
// Otherwise a network error leaves btn.disabled = true forever and the
// user has no retry path.
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
	// Transport-failure recovery: the rollback POST must be wrapped in a
	// try/catch so a network error re-enables the button.
	if !strings.Contains(src, "Rollback failed: network error") {
		t.Fatal("app-detail.js: rollback handler must catch transport errors with a `Rollback failed: network error` message and re-enable the button — otherwise btn.disabled = true sticks forever")
	}
	// 401 must route through ctx.onUnauthorized so the user sees the login
	// view instead of a silent stuck state.
	if !strings.Contains(src, "ctx.onUnauthorized()") {
		t.Fatal("app-detail.js: rollback handler must route 401 through ctx.onUnauthorized() so an expired session falls back to the login view")
	}
}

// TestDeploymentsLoadDoesNotMask404AsEmpty guards the deployments tab error
// surface. The server (internal/api/apps.go handleListDeployments) returns
// `200 []` for an existing app with no deployments, and only emits 404 when
// the app is missing or the user has no view access (via requireViewApp).
// Treating any 404 as "No deployments yet" therefore hides real authorization
// or routing errors as a benign empty state. The buggy block was
//
//	if (resp.status === 404) {
//	  empty.hidden = false;
//	  list.hidden = true;
//	  return;
//	}
//
// directly above the generic !resp.ok branch in the deployments load(). It
// must not return — 404 should fall into the !resp.ok branch so the server's
// error envelope is shown. We search for the conjunction of a 404 check and
// an `empty.hidden = false` assignment within a small window so the test
// doesn't false-positive on the legitimate "GET /api/apps/:slug returned 404
// → navigate home" branch in mount().
func TestDeploymentsLoadDoesNotMask404AsEmpty(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "views/app-detail.js")
	if err != nil {
		t.Fatalf("read app-detail.js: %v", err)
	}
	src := string(b)
	// Walk every `resp.status === 404` occurrence and check whether the next
	// ~120 bytes contain `empty.hidden = false`. That pairing is unique to the
	// deployments-load bug.
	rest := src
	for {
		i := strings.Index(rest, "resp.status === 404")
		if i < 0 {
			break
		}
		end := i + 120
		if end > len(rest) {
			end = len(rest)
		}
		if strings.Contains(rest[i:end], "empty.hidden = false") {
			t.Fatal("app-detail.js: deployments load() must not map `resp.status === 404` to an empty state — handleListDeployments returns 200 [] for empty, so 404 means missing app / no view access and must surface as an error via the !resp.ok branch")
		}
		rest = rest[i+len("resp.status === 404"):]
	}
}

// TestNewUserSnippetIsRunnable guards the new-user handoff. The snippet is
// shown to the admin who creates a new user and shared via Slack/email with
// the recipient; the recipient must be able to paste it into a shell and have
// it work. Two failure modes drove the fix:
//
//  1. The original snippet was `shinyhub login --host X --username Y` with no
//     password flag and no prompt — the recipient got "login failed: 401" and
//     no hint about what to do. The CLI now prompts interactively for a
//     missing password (see internal/cli/login.go), so this snippet is
//     runnable as-is.
//
//  2. The snippet must not include `--password <value>` because that leaks
//     the password into shell history (and into the clipboard via the copy
//     button). Generating a snippet with a literal password would be a
//     regression.
//
// We assert the renderer emits the prompt-friendly form and never the
// password-baked form.
func TestNewUserSnippetIsRunnable(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "shinyhub login --host ${origin} --username ${username}") {
		t.Fatal("app.js renderNewUserSnippet must emit `shinyhub login --host ${origin} --username ${username}` so the new user can paste-and-run; the CLI prompts for the missing password (see internal/cli/login.go runLogin)")
	}
	// Belt and braces: no `--password ` form should be produced anywhere in
	// the rendered snippets — that would leak credentials into shell history
	// and the clipboard.
	if strings.Contains(src, "--password ${") || strings.Contains(src, "--password \"") {
		t.Fatal("app.js: handoff snippets must not include `--password <value>`; the CLI prompts interactively, and embedding the password leaks it into shell history and the clipboard")
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

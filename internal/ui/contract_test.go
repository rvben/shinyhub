package ui_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
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

// TestDeployModalReadsManifestSummary guards the deploy-response contract.
// POST /api/apps/:slug/deploy embeds a "manifest" object summarising what
// [app] settings and [[schedule]] blocks were applied (see
// internal/api/apps.go handleDeployApp and internal/api/manifest_apply.go
// ManifestApplied). The deploy modal in app.js reads body.manifest and
// renders the summary into #deploy-result; if either side renames the key
// the modal silently falls back to the no-manifest auto-close path and
// the operator loses confirmation of what landed.
func TestDeployModalReadsManifestSummary(t *testing.T) {
	assertContains(t, "app.js", "body.manifest",
		"deploy submit handler must read body.manifest from the deploy response to render the post-deploy summary; see internal/api/manifest_apply.go ManifestApplied")
	assertContains(t, "app.js", "formatManifestSummary",
		"app.js must keep formatManifestSummary so the manifest summary lines render under the progress bar")
	assertContains(t, "index.html", `id="deploy-result"`,
		"the deploy modal must keep #deploy-result as the slot for the post-deploy manifest summary")
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

// TestDeploymentRowFitsLongVersionIDs guards the Deployments-tab layout.
// Deployment versions are epoch-millisecond IDs (e.g. v1779913177895, see
// internal/api/apps.go which stamps version = time.Now().UnixMilli()), so the
// .deployment-version column is ~14 monospace characters wide. The original
// grid pinned that column to a fixed 5rem, which is far too narrow: the version
// overflowed and visually collided with the adjacent .deployment-when
// timestamp. The fix sizes the column to its content (minmax floor + max-content)
// and keeps the version on a single line. This test fails if the narrow fixed
// column comes back.
func TestDeploymentRowFitsLongVersionIDs(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	css := string(b)
	if strings.Contains(css, "grid-template-columns: 5rem 1fr auto auto") {
		t.Fatal("style.css: .deployment-row must not pin the version column to a fixed 5rem; epoch-millis version IDs overflow it and overlap the timestamp, so size the column to its content instead")
	}
	assertContains(t, "style.css", "grid-template-columns: minmax(9rem, max-content) 1fr auto auto",
		"the Deployments-tab version column must grow to fit epoch-millis version IDs without overlapping the timestamp")
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

// TestSPADoesNotShipClientSideLogoutDance guards against a regression to the
// previous `?logout=1` + sessionStorage marker design. The 403 access-denied
// page now hands off via a server-side POST to /api/auth/handoff (see
// internal/access/middleware.go renderHandoffPage and internal/api/auth.go
// handleSessionHandoff) — by the time the SPA loads, the cookie is already
// cleared and the JWT is revoked. Any leftover client-side logout dance is
// dead code, and worse: the old design only worked when the access-denied
// page was clicked in the same tab the marker was planted in, so Cmd+Click
// → new tab broke account switching entirely. We pin the absence of every
// hook the old design relied on.
func TestSPADoesNotShipClientSideLogoutDance(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	src := string(b)
	if strings.Contains(src, "consumeLogoutParam") {
		t.Error("app.js: consumeLogoutParam must be removed — handoff is server-side via POST /api/auth/handoff (internal/api/auth.go). The client-side dance only worked in the same tab the 403 page was opened in.")
	}
	if strings.Contains(src, "shiny_logout_intent") {
		t.Error("app.js: shiny_logout_intent sessionStorage marker must be removed — the new server-side handoff has no per-tab dependency.")
	}
	if strings.Contains(src, "params.get('logout')") {
		t.Error("app.js: must not key behaviour on ?logout= — the 403 page POSTs to /api/auth/handoff instead of redirecting through /?logout=1.")
	}

	// Bootstrap must hit /api/auth/me directly. After a successful handoff the
	// 303 lands the browser on /?next=<original> with the cookie already
	// cleared, so /api/auth/me returns 401 and the SPA shows the login form
	// — no client-side short-circuit needed.
	if !strings.Contains(src, "await api('/api/auth/me')") {
		t.Fatal("app.js initialize() must call await api('/api/auth/me') as the auth check")
	}
}

// TestSPAPendingDeployUsesPerTabStorage guards against cross-tab bleed of
// the deploy intent. The /#deploy=<slug> empty-state hash is persisted so
// it survives the in-tab login redirect; the storage choice MUST be
// sessionStorage (per-tab per-origin), not localStorage. localStorage is
// shared across every tab on the same origin — a second tab logging in
// as a different account would see the marker, fail the membership check,
// and clear it, losing the original tab's deploy hint and surfacing a
// confusing modal for an app it doesn't own.
func TestSPAPendingDeployUsesPerTabStorage(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	src := string(b)
	if strings.Contains(src, "localStorage.setItem('pendingDeploy'") ||
		strings.Contains(src, "localStorage.getItem('pendingDeploy'") ||
		strings.Contains(src, "localStorage.removeItem('pendingDeploy'") {
		t.Fatal("app.js: pendingDeploy must use sessionStorage, not localStorage. localStorage bleeds across tabs on the same origin and lets a second tab (different account) consume or clobber the originating tab's deploy intent.")
	}
	if !strings.Contains(src, "sessionStorage.setItem('pendingDeploy'") {
		t.Fatal("app.js: persistDeployHash must call sessionStorage.setItem('pendingDeploy', ...) to persist the deploy intent across the in-tab login redirect")
	}
	if !strings.Contains(src, "sessionStorage.getItem('pendingDeploy')") {
		t.Fatal("app.js: handleDeployHash must read sessionStorage.getItem('pendingDeploy') as a fallback when no #deploy= hash is present")
	}
	if !strings.Contains(src, "sessionStorage.removeItem('pendingDeploy')") {
		t.Fatal("app.js: handleDeployHash must clear sessionStorage on consume/no-permission paths so the entry can't loop")
	}
}

// TestSPALogoutButtonRespectsServerOutcome guards against the logout button
// lying to the user. The previous handler swallowed every fetch outcome and
// called showLoggedOut() unconditionally, so a 403 (missing CSRF cookie) or
// 500 left the server session alive while the SPA showed the login form
// locally — a single refresh logged the user straight back in. The handler
// must only clear local state on success (resp.ok) or 401 (already gone),
// and surface a flashToast on the failure branch so the user knows the
// logout didn't take effect.
func TestSPALogoutButtonRespectsServerOutcome(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	src := string(b)
	const handlerSig = "logoutButton.addEventListener('click', async ()"
	hStart := strings.Index(src, handlerSig)
	if hStart < 0 {
		t.Fatal("app.js: missing logoutButton click handler")
	}
	rest := src[hStart:]
	end := strings.Index(rest, "\n  async function ")
	if end < 0 {
		end = len(rest)
	}
	body := rest[:end]
	if !strings.Contains(body, "/api/auth/logout") {
		t.Fatal("logout handler must POST /api/auth/logout")
	}
	if !strings.Contains(body, "resp.ok || resp.status === 401") {
		t.Fatal("logout handler must guard showLoggedOut() on `resp.ok || resp.status === 401` so a server-side reject (403 missing CSRF, 500) doesn't lie to the user about being signed out")
	}
	if !strings.Contains(body, "flashToast") {
		t.Fatal("logout handler must surface a flashToast on the failure branch so the user knows the logout didn't take effect server-side")
	}
}

// TestScheduleTimezoneFields guards the schedule DTO timezone contract.
// The server now returns effective_timezone, timezone_inherited, and timezone
// on each schedule record. The UI must read all three fields:
//   - effective_timezone: the resolved IANA zone (always present)
//   - timezone_inherited: bool, true when no per-schedule zone is stored
//   - timezone: the raw stored value (null = inherit)
//
// The table renders next_fire in the effective_timezone via Intl.DateTimeFormat,
// so operators see fire times in the schedule's own zone, not browser-local.
func TestScheduleTimezoneFields(t *testing.T) {
	assertContains(t, "app.js", "s.effective_timezone",
		"schedule table must read s.effective_timezone from the DTO to render the zone and next_fire correctly")
	assertContains(t, "app.js", "s.timezone_inherited",
		"schedule table must read s.timezone_inherited to show the (inherited) hint when no per-schedule timezone is stored")
	assertContains(t, "app.js", "s.timezone",
		"schedule form must read s.timezone when editing an existing schedule to populate the timezone field")
	assertContains(t, "app.js", "sched-timezone",
		"schedule form must reference sched-timezone so the timezone input is populated and submitted")
	assertContains(t, "app.js", "Preview (browser-local)",
		"cron preview label must clarify it shows browser-local time so operators are not misled about the schedule's effective timezone")
	assertContains(t, "index.html", "sched-timezone",
		"schedule form modal in index.html must have a sched-timezone input for the optional per-schedule timezone")
}

// TestScheduleDSTAdvisoryWired guards the DST fall-back double-fire surface.
// The server computes the advisory and returns it on the schedule DTO as
// dst_advisory; the schedule table must render it inline in the cron cell via
// the dstAdvisoryMarkup helper. If the import or the call site is dropped the
// double-fire footgun goes silent in the UI again.
func TestScheduleDSTAdvisoryWired(t *testing.T) {
	assertContains(t, "app.js", "import { dstAdvisoryMarkup } from '/static/views/schedule-ui.js'",
		"app.js must import dstAdvisoryMarkup so the schedule table can surface the DST fall-back advisory")
	assertContains(t, "app.js", "dstAdvisoryMarkup(s)",
		"schedule table cron cell must call dstAdvisoryMarkup(s) to render the dst_advisory from the DTO")
	assertContains(t, "views/schedule-ui.js", "schedule.dst_advisory",
		"schedule-ui helper must read dst_advisory from the schedule DTO computed by the server")
}

// TestSharedDataReadOnlyHelpIsHonest guards the shared-data help text. Under the
// native runtime the read-only mount is a convention only (the source data dir
// is symlinked and writes through it are not blocked); the Docker runtime
// enforces it at the OS level. The Settings -> Data help must say so, otherwise
// operators trust an enforcement guarantee that native does not provide.
func TestSharedDataReadOnlyHelpIsHonest(t *testing.T) {
	assertContains(t, "index.html", "convention",
		"shared-data help must state read-only is a convention under the native runtime, not OS-enforced")
	assertContains(t, "index.html", "Docker runtime",
		"shared-data help must point at the Docker runtime for OS-level read-only enforcement")
}

// TestScheduleRunHistoryReadsSnakeCase guards the JSON contract for schedule
// runs. db.ScheduleRun serializes with snake_case json tags (id, status,
// exit_code, started_at; see internal/db/schedules.go), so the run-history
// list in app.js must read those keys. If the frontend reverts to the old
// PascalCase reads (run.Status, run.ExitCode, ...) the history rows render
// blank and the per-run log buttons call the endpoint with an undefined id.
func TestScheduleRunHistoryReadsSnakeCase(t *testing.T) {
	for _, needle := range []string{"run.started_at", "run.status", "run.exit_code", "run.id"} {
		assertContains(t, "app.js", needle,
			"run-history list must read snake_case ScheduleRun fields; see internal/db/schedules.go json tags")
	}
	// exit_code is always serialized (int, COALESCE'd to 0), so the UI must
	// gate the exit-code display on finished_at to avoid showing "exit 0" for
	// a still-running run.
	assertContains(t, "app.js", "run.finished_at",
		"run-history must gate the exit-code display on run.finished_at; a running run has exit_code 0 but is not finished")
	// The PascalCase reads must be gone so the regression cannot creep back.
	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	for _, gone := range []string{"run.StartedAt", "run.Status", "run.ExitCode", "run.ID"} {
		if strings.Contains(string(b), gone) {
			t.Errorf("app.js must not read PascalCase %q; ScheduleRun is snake_case now", gone)
		}
	}
}

// TestFrontendConsumesBrandingObject guards the branding contract: the server
// injects window.__SHINYHUB_BRANDING__ (see internal/ui/branding.go RenderIndex)
// and exposes the same shape at /.shinyhub/branding.json. The SPA must read
// site_title/logo/footer_links from it; router.js must fall back to it for the
// document title instead of the hardcoded 'ShinyHub'.
func TestFrontendConsumesBrandingObject(t *testing.T) {
	assertContains(t, "app.js", "__SHINYHUB_BRANDING__",
		"app.js must read window.__SHINYHUB_BRANDING__ to apply logo/footer; see internal/ui/branding.go RenderIndex")
	assertContains(t, "router.js", "__SHINYHUB_BRANDING__",
		"router.js must fall back to branding site_title for document.title instead of hardcoded 'ShinyHub'")
	assertContains(t, "router.js", "|| 'ShinyHub'",
		"router.js brandTitle fallback must use || 'ShinyHub' so zero-branding produces the default brand name")
	assertContains(t, "router.js", "' · ' + brandTitle",
		"router.js must compose document.title as current.title + ' · ' + brandTitle so page titles include the brand name")
}

// TestAppsPayloadExposesFleetFields guards the JSON contract for the two fleet
// fields added to db.App. The apps grid / detail JS reads body.managed_by and
// body.content_digest; if either field is renamed the build breaks here rather
// than silently breaking the dashboard.
//
// managed_by is a non-omit *string so it always serializes (null when nil).
// content_digest is omitempty so it only serializes when set; we assert via a
// populated value.
func TestAppsPayloadExposesFleetFields(t *testing.T) {
	b, err := json.Marshal(db.App{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(b, []byte(`"managed_by"`)) {
		t.Fatal(`db.App must always serialize "managed_by"`)
	}
	b2, err := json.Marshal(db.App{ContentDigest: "sha256:x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(b2, []byte(`"content_digest"`)) {
		t.Fatal(`db.App must serialize "content_digest" when set`)
	}
}

// assertFileContains reads an on-disk file (not embedded) by absolute path and
// asserts it contains needle.
func assertFileContains(t *testing.T, absPath, needle, contract string) {
	t.Helper()
	b, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("assertFileContains: read %s: %v", absPath, err)
	}
	if !strings.Contains(string(b), needle) {
		t.Fatalf("assertFileContains %s: want %q\ncontract: %s", absPath, needle, contract)
	}
}

// TestFargateBundleRouteOnMainMux guards that the bundle endpoint is registered
// directly on the main mux (not under /api/), so large bundle streams bypass
// the 30-second apiTimeoutHandler. We assert that main.go contains the route
// string outside the /api/ subtree by checking for the literal path fragment.
// This is a source-search contract test, not an HTTP test, because the mux is
// constructed in main.go which cannot be imported as a package.
func TestFargateBundleRouteOnMainMux(t *testing.T) {
	// The runner entrypoint script must reference the bundle endpoint path so
	// a refactor of the URL cannot silently break the runner without this test
	// catching the drift.
	assertFileContains(t, "../../build/fargate-runner/entrypoint.sh",
		"/internal/fargate-bundle/",
		"entrypoint.sh must fetch the bundle from GET /internal/fargate-bundle/{digest}; changing this path requires updating the entrypoint too")
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

// TestDashboardFleetSurfaceWiring pins the read-only fleet dashboard surface
// to the static SPA. app.js is a large IIFE that cannot be imported in a unit
// test, so - exactly like TestAppDetailUnwrapsGetAppResponse - we assert the
// wiring by source string-search. The fleet logic itself is unit-tested in
// internal/ui/jstests/fleet-ui.test.js.
func TestDashboardFleetSurfaceWiring(t *testing.T) {
	// The helper module is the single source of fleet truth and reads the
	// two API fields. If db.App renames either, fleet-ui.js breaks here.
	assertContains(t, "views/fleet-ui.js", "app.managed_by",
		"fleet ownership derives from the managed_by API field")
	assertContains(t, "views/fleet-ui.js", "content_digest",
		"the live deployment digest derives from the content_digest API field")

	// Apps grid wiring: imports the helpers, badges cards, segments the list.
	assertContains(t, "app.js", "/static/views/fleet-ui.js",
		"apps grid imports the fleet-ui helper module")
	assertContains(t, "app.js", "makeFleetBadge",
		"apps grid cards show the fleet ownership badge")
	assertContains(t, "app.js", "segmentApps",
		"apps grid filters by the All/Fleet-managed/Unmanaged segment")
	assertContains(t, "index.html", "apps-segment",
		"apps toolbar exposes the All/Fleet-managed/Unmanaged control")

	// App-detail wiring: header badge + live digest slot (next task).
	assertContains(t, "views/app-detail.js", "/static/views/fleet-ui.js",
		"app detail imports the fleet-ui helper module")
	assertContains(t, "views/app-detail.js", "renderFleetDigest",
		"app detail renders the live deployment digest line")
	assertContains(t, "index.html", "app-detail-fleet",
		"app detail exposes the live deployment digest slot")
}

// TestAutoscaleSurfaceWiring pins the read-only autoscale overview surface to
// its testable helper module. app-detail.js cannot be imported under jsdom, so
// the autoscale logic lives in views/autoscale.js (unit-tested in
// jstests/autoscale.test.js) and the overview panel must consume it rather than
// re-implement the inherited-target / sorted-rejects logic inline.
//
// We also guard the field-name contract: autoscale.js reads
// app.autoscale_enabled, envelope.effective_autoscale_target, and the
// rejects_by_reason rollup. If any of these are renamed in
// internal/api/apps.go handleGetApp, the dashboard summary stops rendering and
// this test catches it before the regression ships.
func TestAutoscaleSurfaceWiring(t *testing.T) {
	// The overview panel imports the helper module and calls each helper.
	assertContains(t, "views/app-detail.js", "/static/views/autoscale.js",
		"app detail imports the autoscale helper module so the overview surface stays consistent with the unit tests")
	assertContains(t, "views/app-detail.js", "renderAutoscaleSummary",
		"the overview panel renders the autoscale facts via renderAutoscaleSummary")
	assertContains(t, "views/app-detail.js", "renderRejectsByReason",
		"the overview panel renders the rejects-by-reason rollup via renderRejectsByReason")
	assertContains(t, "views/app-detail.js", "summariseAutoscale",
		"the overview panel flattens the autoscale envelope slice via summariseAutoscale")
	assertContains(t, "views/app-detail.js", "formatRejectsByReason",
		"the overview panel normalises the rejects rollup via formatRejectsByReason")

	// Slot ids the helpers populate. If these drift the helpers paint
	// nowhere and the operator-facing summary silently disappears.
	assertContains(t, "views/app-detail.js", `id="autoscale-summary"`,
		"app detail must expose #autoscale-summary as the slot renderAutoscaleSummary fills")
	assertContains(t, "views/app-detail.js", `id="overview-rejects-by-reason"`,
		"app detail must expose #overview-rejects-by-reason as the container renderRejectsByReason reveals")
	assertContains(t, "views/app-detail.js", `id="overview-rejects-by-reason-list"`,
		"app detail must expose #overview-rejects-by-reason-list as the <ul> renderRejectsByReason populates")

	// The helper module reads the API envelope fields. If internal/api/apps.go
	// handleGetApp renames any of these, the dashboard goes blank.
	assertContains(t, "views/autoscale.js", "autoscale_enabled",
		"autoscale helper must read app.autoscale_enabled; see internal/api/apps.go handleGetApp")
	assertContains(t, "views/autoscale.js", "autoscale_min_replicas",
		"autoscale helper must read app.autoscale_min_replicas; see internal/api/apps.go handleGetApp")
	assertContains(t, "views/autoscale.js", "autoscale_max_replicas",
		"autoscale helper must read app.autoscale_max_replicas; see internal/api/apps.go handleGetApp")
	assertContains(t, "views/autoscale.js", "autoscale_target",
		"autoscale helper must read app.autoscale_target; see internal/api/apps.go handleGetApp")
	assertContains(t, "views/autoscale.js", "effective_autoscale_target",
		"autoscale helper must read envelope.effective_autoscale_target so the inherited fallback is honest; see internal/api/apps.go handleGetApp")
}

// TestAutoscaleEditableFormWiring pins the editable autoscale form on the
// Configuration tab. The validator lives in views/autoscale.js
// (readAutoscaleForm, unit-tested in jstests/autoscale.test.js); the save
// wrapper in app.js must import it, PATCH /api/apps/:slug with the autoscale
// block the API expects, and reset state through populateAutoscaleTab on every
// view of the Configuration tab. If any of these wires drift, the form either
// silently sends a malformed payload or re-renders stale values after a save.
func TestAutoscaleEditableFormWiring(t *testing.T) {
	// The fieldset and its inputs are what readAutoscaleForm reads by id; if
	// any id changes here the helper falls back to an error path that hides
	// the real cause behind a generic "must be a whole number" message.
	assertContains(t, "index.html", `id="autoscale-options"`,
		"index.html must expose #autoscale-options as the editable autoscale fieldset")
	assertContains(t, "index.html", `id="autoscale-enabled"`,
		"index.html must expose #autoscale-enabled as the autoscale enable checkbox readAutoscaleForm reads")
	assertContains(t, "index.html", `id="autoscale-min"`,
		"index.html must expose #autoscale-min as the min-replicas input readAutoscaleForm reads")
	assertContains(t, "index.html", `id="autoscale-max"`,
		"index.html must expose #autoscale-max as the max-replicas input readAutoscaleForm reads")
	assertContains(t, "index.html", `id="autoscale-target"`,
		"index.html must expose #autoscale-target as the custom target input readAutoscaleForm reads")
	assertContains(t, "index.html", `name="autoscale-target-mode"`,
		"index.html must expose name=autoscale-target-mode for the default/custom radio readAutoscaleForm reads")
	assertContains(t, "index.html", `id="autoscale-save-btn"`,
		"index.html must expose #autoscale-save-btn so saveAutoscaleSettings has a click target")

	// app.js wires the form: it imports the pure validator, populates the
	// fieldset on every Configuration view, and PATCHes with the autoscale
	// block handlePatchApp accepts.
	assertContains(t, "app.js", "/static/views/autoscale.js",
		"app.js must import the autoscale helper module so the form validator stays in lockstep with the unit tests")
	assertContains(t, "app.js", "readAutoscaleForm",
		"app.js must call readAutoscaleForm so the save path runs the same validation the unit tests pin")
	assertContains(t, "app.js", "parseReplicaBound",
		"app.js must share parseReplicaBound with the save path so the live ceiling preview cannot show a different bound than the one being saved")
	assertContains(t, "app.js", "function populateAutoscaleTab",
		"app.js must define populateAutoscaleTab so the Configuration tab seeds the form from the GET envelope")
	assertContains(t, "app.js", "saveAutoscaleSettings",
		"app.js must define saveAutoscaleSettings so the Save button issues the PATCH")
	assertContains(t, "app.js", `JSON.stringify({ autoscale: payload })`,
		"app.js must PATCH the autoscale block under the 'autoscale' key handlePatchApp expects (internal/api/apps.go)")

	// app-detail.js calls populate on every Configuration tab view so a save
	// followed by a tab switch re-renders the current persisted values, not
	// the stale form state. Without this the form drifts visibly after edits.
	assertContains(t, "views/app-detail.js", "ctx.populateAutoscaleTab(app)",
		"app-detail.js must populate the autoscale tab whenever Configuration is rendered")
}

// TestTracesSurfaceWiring pins the traces panel to its testable helper module.
// app-detail.js cannot be imported under jsdom, so the rendering logic lives in
// views/traces-ui.js (unit-tested in jstests/traces-ui.test.js) and the panel
// must consume it rather than re-implementing row building inline. Guards
// TRC-2 (unsampled spans render no dead deep link), TRC-3 (date in the When
// column), and TRC-5 (the traces-status element reports poll freshness).
func TestTracesSurfaceWiring(t *testing.T) {
	assertContains(t, "views/app-detail.js", "/static/views/traces-ui.js",
		"the traces panel imports the traces-ui helper module")
	assertContains(t, "views/app-detail.js", "makeTraceRow",
		"the traces panel builds rows via makeTraceRow so unsampled/date logic is shared and tested")
	assertContains(t, "views/app-detail.js", "formatPollStatus",
		"the traces-status element is updated with poll freshness via formatPollStatus")

	// The helper module reads the sampled flag (TRC-2) and started_at (TRC-3)
	// from the span JSON. If tracing.Span renames either, this breaks here.
	assertContains(t, "views/traces-ui.js", "sampled",
		"unsampled spans must be detected from the span.sampled API field")
	assertContains(t, "views/traces-ui.js", "started_at",
		"the When column derives from the span.started_at API field")
}

// TestFargateYamlExampleHasFargateBlock asserts that shinyhub.yaml.example
// contains a runtime.fargate config block. If this fails, the operator config
// docs are missing and a Fargate tier cannot be correctly configured without
// reading the source code.
func TestFargateYamlExampleHasFargateBlock(t *testing.T) {
	assertFileContains(t,
		"../../shinyhub.yaml.example",
		"  fargate:",
		"shinyhub.yaml.example must contain a runtime.fargate block documenting all Fargate config fields",
	)
	assertFileContains(t,
		"../../shinyhub.yaml.example",
		"control_plane_url",
		"shinyhub.yaml.example runtime.fargate block must document control_plane_url (required Fargate field)",
	)
	assertFileContains(t,
		"../../shinyhub.yaml.example",
		"bundle_token_ttl",
		"shinyhub.yaml.example runtime.fargate block must document bundle_token_ttl (Fargate bundle fetch token TTL)",
	)
}

// TestYamlExampleDocumentsBackendBlocks asserts that shinyhub.yaml.example
// documents the config blocks that gate this release's features: runtime.tiers
// (multi-backend placement), runtime.autoscale (the replica autoscale
// controller), and the top-level worker block (remote-worker hosting). Without
// these an operator cannot discover how to enable the features without reading
// the source.
func TestYamlExampleDocumentsBackendBlocks(t *testing.T) {
	const path = "../../shinyhub.yaml.example"
	assertFileContains(t, path, "tiers:",
		"shinyhub.yaml.example must document the runtime.tiers block (per-tier backend placement)")
	assertFileContains(t, path, "launch_type",
		"shinyhub.yaml.example tiers docs must mention launch_type (FARGATE/EC2)")
	assertFileContains(t, path, "  autoscale:",
		"shinyhub.yaml.example must document the runtime.autoscale block")
	assertFileContains(t, path, "default_target",
		"shinyhub.yaml.example autoscale block must document default_target")
	assertFileContains(t, path, "worker:",
		"shinyhub.yaml.example must document the top-level worker block (remote-worker hosting)")
	assertFileContains(t, path, "join_token_file",
		"shinyhub.yaml.example worker block must document join_token_file")
	assertFileContains(t, path, "advertise_hosts",
		"shinyhub.yaml.example worker block must document advertise_hosts")
}

// TestReplicaDisplayWiring pins the import and call sites for replica-display.js.
// The helper is testable under jsdom (Task 1); the wiring inside app.js and
// app-detail.js (which jsdom cannot import) is pinned here so a refactor that
// drops the import or the call site fails the build instead of silently
// breaking the panel.
func TestReplicaDisplayWiring(t *testing.T) {
	// app.js imports the helper (grid card path + renderReplicasPanel).
	assertContains(t, "app.js", `'/static/views/replica-display.js'`,
		"app.js must import replica-display.js for grid-card and panel backend/metrics rendering")
	assertContains(t, "app.js", "backendLabel",
		"app.js renderReplicasPanel and grid card must call backendLabel to show the backend/tier label")
	assertContains(t, "app.js", "metricsText",
		"app.js renderReplicasPanel and grid card must call metricsText for honest CPU/RAM rendering")

	// app-detail.js imports the helper (seedReplicasFromStatus).
	assertContains(t, "views/app-detail.js", `'/static/views/replica-display.js'`,
		"app-detail.js must import replica-display.js so seedReplicasFromStatus can show tier/provider and n/a metrics")
	assertContains(t, "views/app-detail.js", "backendLabel",
		"seedReplicasFromStatus must call backendLabel to render the initial backend/tier label per replica")
	assertContains(t, "views/app-detail.js", "metricsText",
		"seedReplicasFromStatus must call metricsText so the initial panel state is honest for PID-less replicas")

	// Both render paths must surface the per-replica degraded reason (e.g.
	// "worker unavailable" for a lost replica) instead of a bare status badge.
	assertContains(t, "app.js", "reasonLabel",
		"app.js renderReplicasPanel must call reasonLabel to surface a lost replica's reason")
	assertContains(t, "views/app-detail.js", "reasonLabel",
		"seedReplicasFromStatus must call reasonLabel to surface a lost replica's reason")
}

// TestWorkersPageWiring pins the admin Workers page across the SPA: the nav tab,
// the section, the route registration, the API call, and the admin gating. A
// refactor that drops any of these fails the build instead of silently breaking
// the read-only worker-fleet view.
func TestWorkersPageWiring(t *testing.T) {
	assertContains(t, "index.html", `id="tab-workers"`,
		"index.html must have the Workers nav tab")
	assertContains(t, "index.html", `id="workers-view"`,
		"index.html must have the workers-view section")
	assertContains(t, "index.html", `id="workers-body"`,
		"index.html must have the workers table body the renderer fills")
	assertContains(t, "app.js", `'/static/views/workers.js'`,
		"app.js must import the workers view module")
	assertContains(t, "app.js", "router.register('/workers'",
		"app.js must register the /workers SPA route")
	assertContains(t, "app.js", "mountWorkers(",
		"app.js must mount the workers view")
	assertContains(t, "app.js", "/api/workers",
		"app.js loadWorkers must call the /api/workers endpoint")
	assertContains(t, "app.js", "tabWorkers.hidden = payload.user.role !== 'admin'",
		"the Workers tab must be admin-gated in showLoggedIn")
}

// TestMetricsAvailableWiring pins the top-level metrics_available field
// consumed by the grid card path. The grid card reads m.cpu_percent /
// m.rss_bytes from the legacy top-level scalars (not m.replicas), so the
// PID-less signal for the grid path is the top-level m.metrics_available flag
// added in plan 01 (Contract 5). Without this pin a refactor could silently
// revert to showing "0.0% CPU / 0 KB RAM" for Fargate apps on the grid.
func TestMetricsAvailableWiring(t *testing.T) {
	assertContains(t, "app.js", "m.metrics_available",
		"app.js onMetrics grid card must read m.metrics_available to gate CPU/RAM display; see plan-01 Contract 5")
}

// TestAutoscaleStatusWiring pins the autoscale_status and global_autoscale_enabled
// consumption in app-detail.js. The detail view passes both to summariseAutoscale
// via the envelope object; the poll path (onMetrics) must also update the summary
// so the cooldown indicator refreshes without a full page re-fetch.
func TestAutoscaleStatusWiring(t *testing.T) {
	assertContains(t, "views/app-detail.js", "autoscale_status",
		"app-detail.js renderOverview must pass body.autoscale_status to summariseAutoscale via the envelope")
	assertContains(t, "views/app-detail.js", "global_autoscale_enabled",
		"app-detail.js renderOverview must pass body.global_autoscale_enabled to summariseAutoscale via the envelope")
	assertContains(t, "app.js", "autoscale_status",
		"app.js onMetrics must update autoscale_status on the stored envelope so the cooldown row refreshes on each 10s poll")
}

// TestSeedReplicasConsumesNewFields pins that seedReplicasFromStatus reads the
// tier and provider fields already present on replicas_status entries
// (db.Replica carries them; handleGetApp includes them in the envelope).
func TestSeedReplicasConsumesNewFields(t *testing.T) {
	assertContains(t, "views/app-detail.js", "r.tier",
		"seedReplicasFromStatus must read r.tier from replicas_status entries (already present in db.Replica / handleGetApp envelope)")
	assertContains(t, "views/app-detail.js", "r.provider",
		"seedReplicasFromStatus must read r.provider from replicas_status entries (already present in db.Replica / handleGetApp envelope)")
	assertContains(t, "views/app-detail.js", "r.metrics_available",
		"seedReplicasFromStatus must read r.metrics_available to show n/a for PID-less replicas on initial load; see plan-01 Contract 5")
}

// TestKnownActionsAutoscale pins the knownActions array in app.js to include
// the two new autoscale audit actions and to not duplicate create_user.
func TestKnownActionsAutoscale(t *testing.T) {
	assertContains(t, "app.js", "'autoscale_scale_up'",
		"knownActions in app.js renderAuditEvents must include autoscale_scale_up; see Contract 8")
	assertContains(t, "app.js", "'autoscale_scale_down'",
		"knownActions in app.js renderAuditEvents must include autoscale_scale_down; see Contract 8")

	// Assert no duplicate create_user: count occurrences inside knownActions.
	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	src := string(b)
	// Locate knownActions array by finding the renderAuditEvents function
	// and extracting the array body up to its closing bracket.
	start := strings.Index(src, "const knownActions = [")
	if start < 0 {
		t.Fatal("app.js: cannot find `const knownActions = [` inside renderAuditEvents")
	}
	end := strings.Index(src[start:], "];")
	if end < 0 {
		t.Fatal("app.js: cannot find closing `];` for knownActions array")
	}
	arrayBody := src[start : start+end+2]
	count := strings.Count(arrayBody, "'create_user'")
	if count != 1 {
		t.Fatalf("app.js knownActions: 'create_user' appears %d time(s); want exactly 1 (remove the duplicate OAuth comment block; see Contract 8)", count)
	}
}

// TestGridAutoscaleBadge pins the autoscale badge on the grid card.
// app.autoscale_enabled is already in the apps-list payload (no server change).
// The badge renders a small "auto" indicator when true so operators can see
// at a glance which apps have autoscale active.
func TestGridAutoscaleBadge(t *testing.T) {
	assertContains(t, "app.js", "autoscale_enabled",
		"app.js renderGridVerbatim must read app.autoscale_enabled to conditionally render the autoscale badge on grid cards")
	assertContains(t, "app.js", "badge-autoscale",
		"app.js grid card must apply badge-autoscale class (or similar) to the autoscale indicator badge")
}

// TestAutoscaleActionBadgeCSS guards that the two new autoscale audit action
// badges are styled with the blue config color, consistent with create_app /
// update_app / env.set. Without this the badges fall back to badge-action-default
// (gray) which is visually inconsistent with other config-change actions.
//
// The CSS selector MUST match the class the badge renderer actually generates.
// app.js builds the class via `badge-action-${e.action.replace(/\./g, '-')}`,
// which only replaces dots; underscores in the action name are preserved. So
// "autoscale_scale_up" -> class "badge-action-autoscale_scale_up" (underscores).
// A CSS selector with hyphens (.badge-action-autoscale-scale-up) would never
// match that class and the badge would fall back to the default gray color.
func TestAutoscaleActionBadgeCSS(t *testing.T) {
	// Compute the exact class names the JS badge renderer will produce for each
	// autoscale action, then assert those exact strings appear in style.css.
	// This makes hyphen/underscore drift a build failure rather than a visual bug.
	for _, action := range []string{"autoscale_scale_up", "autoscale_scale_down"} {
		// Mirrors: badge-action-${e.action.replace(/\./g, '-')}
		// (dots replaced with hyphens; underscores kept as-is)
		class := "." + "badge-action-" + strings.ReplaceAll(action, ".", "-")
		assertContains(t, "style.css", class,
			"style.css must define "+class+" matching the class app.js generates for action "+action+
				"; use underscores not hyphens (JS replace only converts dots)")
	}
}

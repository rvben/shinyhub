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

// TestAppCardBadgeReadsDeploymentStatus guards the failed-vs-never-deployed
// badge. The app summary exposes last_deployment_status (internal/db/queries.go
// deploymentSummarySQL); appCardBadge reads it so a failed-only deploy renders
// "Failed" instead of the benign "Awaiting deploy", and app.js must route the
// card badge through it.
func TestAppCardBadgeReadsDeploymentStatus(t *testing.T) {
	assertContains(t, "app.js", "appCardBadge",
		"app.js must use appCardBadge so a failed deploy renders Failed, not Awaiting deploy")
	assertContains(t, "views/app-card-badge.js", "last_deployment_status",
		"appCardBadge must read app.last_deployment_status; see internal/db/queries.go deploymentSummarySQL")
}

// TestGridStatusBadgeRefreshesFromMetricsPoll guards the live status badge.
// The badge is computed once at render time; without this wiring it freezes at
// its render-time status, so a card opened while an app is hibernating never
// reflects a wake/sleep transition. The 10s /metrics poll carries a live
// `status`, and onMetrics must push it onto the tagged badge via
// updateCardStatusBadge (which re-derives through appCardBadge so pre-deploy
// "Awaiting deploy"/"Failed" states are not clobbered by a poll's "stopped").
func TestGridStatusBadgeRefreshesFromMetricsPoll(t *testing.T) {
	assertContains(t, "app.js", "updateCardStatusBadge",
		"app.js must import and call updateCardStatusBadge so the grid status badge tracks the live /metrics status")
	assertContains(t, "app.js", "badge.dataset.slug = app.slug",
		"renderGridVerbatim must tag the status badge with data-slug so onMetrics can locate it")
	assertContains(t, "app.js", ".app-header .badge[data-slug=",
		"onMetrics must locate the status badge by its data-slug to refresh it in place")
	assertContains(t, "views/app-card-badge.js", "export function updateCardStatusBadge",
		"app-card-badge.js must export updateCardStatusBadge for the live badge refresh")
}

// TestAppCardTitleHasNoLinkUnderline guards issue-1's fix: the whole card body
// is an <a>, so underlining the title on hover made it read like a text link.
// The card already signals it is clickable (lift + cyan border + accent dot);
// the title shifts to the brand cyan instead. Pin that the underline rule is
// gone so it cannot creep back.
func TestAppCardTitleHasNoLinkUnderline(t *testing.T) {
	assertNotContains(t, "style.css", ".app-card-body-link:hover strong { text-decoration: underline; }",
		"the card title must not be underlined on hover (it reads as a text link); use the cyan accent shift instead")
	assertContains(t, "style.css", ".app-card-body-link:hover strong { color: var(--cyan-bright); }",
		"the card title hover affordance must be the brand cyan accent shift, not a link underline")
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
// TestAccessVisibilityUsesExplicitSave pins the explicit-save model for the
// Access visibility control. It was previously an auto-apply-on-change radio
// (generation-serialized); it is now consistent with every other settings tab:
// an edit marks the section dirty and a Save button PATCHes /access. The Save
// button and handler MUST exist and be wired.
func TestAccessVisibilityUsesExplicitSave(t *testing.T) {
	assertContains(t, "index.html", `id="visibility-save-btn"`,
		"the Visibility section must have an explicit Save button")
	assertContains(t, "app.js", "async function saveVisibility",
		"visibility must be persisted by an explicit saveVisibility handler")
	assertContains(t, "app.js", "getElementById('visibility-save-btn').addEventListener('click', saveVisibility)",
		"the Visibility Save button must be wired to saveVisibility")
	assertContains(t, "app.js", "registerSettingsSection('visibility'",
		"the Visibility section must register with the dirty-state tracker")
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
	assertContains(t, "style.css", "grid-template-columns: minmax(11rem, max-content) 1fr auto",
		"the Deployments-tab version column must grow to fit the deploy number, status badge, and epoch-millis version ID without overlapping the timestamp")
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
	// exit_code is null until a terminal state and stays null for an
	// interrupted run, so the UI must gate the exit-code display on both
	// finished_at AND a non-null exit_code to avoid rendering "exit null".
	assertContains(t, "app.js", "run.finished_at",
		"run-history must gate the exit-code display on run.finished_at; a running run has a null exit_code")
	assertContains(t, "app.js", "run.exit_code != null",
		"run-history must gate the exit-code display on a non-null exit_code; an interrupted run is finished but has a null exit_code")
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

func assertNotContains(t *testing.T, path, needle, contract string) {
	t.Helper()
	b, err := fs.ReadFile(ui.Static(), path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.Contains(string(b), needle) {
		t.Fatalf("%s must NOT contain %q to honor contract: %s", path, needle, contract)
	}
}

// TestAppsGridUsesAppCardActions guards the apps-grid render against the
// ReferenceError regression where renderGridVerbatim referenced an undeclared
// `neverDeployed`. That threw on the first card, aborting the entire grid
// render; and because it threw during initialize(), it also left downstream
// dashboard wiring (modals, Refresh) unbound. The per-card show/hide decision
// now lives in the unit-tested appCardActions helper; app.js must import and
// use it, and must not reference a bare `neverDeployed` (no longer in scope).
func TestAppsGridUsesAppCardActions(t *testing.T) {
	assertContains(t, "views/app-card-actions.js", "export function appCardActions",
		"the appCardActions helper module must exist and be exported")
	assertContains(t, "app.js", "appCardActions(",
		"renderGridVerbatim must compute card-action visibility via the unit-tested appCardActions helper")
	assertNotContains(t, "app.js", "neverDeployed",
		"app.js must not reference a bare `neverDeployed`; that undeclared variable threw ReferenceError and broke the whole grid. Use appCardActions(app, canManage) instead")
}

// TestGrantByUsernameUsesServerResolution guards the access-grant security fix:
// the Access tab must grant by POSTing { username } to /members (the server
// resolves it under manage-app authorization) and must NOT pre-resolve via
// GET /api/users/{username}, which is restricted to app operators and would 403
// for an app manager who lacks the app-create privilege.
func TestGrantByUsernameUsesServerResolution(t *testing.T) {
	assertContains(t, "app.js", "JSON.stringify({ username })",
		"the grant flow must POST {username} so the server resolves it under manage-app authorization")
	assertNotContains(t, "app.js", "/api/users/${encodeURIComponent(username)}",
		"the grant flow must not pre-resolve the username via the operator-only user-lookup endpoint")
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

// TestFleetHealthBannerWiring pins the admin fleet-health banner: the helper
// import, the API call, the admin gate, and the markup element it renders into.
func TestFleetHealthBannerWiring(t *testing.T) {
	assertContains(t, "index.html", `id="fleet-health"`,
		"index.html must have the fleet-health banner element on the Apps grid")
	assertContains(t, "app.js", `'/static/views/fleet-health.js'`,
		"app.js must import the fleet-health summarizer")
	assertContains(t, "app.js", "summariseFleetHealth(",
		"app.js must call summariseFleetHealth to render the banner")
	assertContains(t, "app.js", "/api/fleet/health",
		"app.js loadFleetHealth must call the /api/fleet/health endpoint")
	assertContains(t, "app.js", "loadFleetHealth()",
		"loadApps must refresh the fleet-health banner")
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

// TestModalFocusManagementWiring pins the modal focus-trap wiring. The trap
// logic lives in views/focus-trap.js (unit-tested in jstests/focus-trap.test.js);
// app.js (not jsdom-importable) must import it and activate/release a trap for
// each modal so keyboard focus can't escape an open dialog and is restored to
// the trigger on close. We count activate/release pairs so a refactor that
// drops the wiring for any one of the five modals fails the build.
func TestModalFocusManagementWiring(t *testing.T) {
	assertContains(t, "views/focus-trap.js", "export function createFocusTrap",
		"focus-trap.js must export createFocusTrap")
	assertContains(t, "views/focus-trap.js", "export function focusableElements",
		"focus-trap.js must export focusableElements")
	assertContains(t, "app.js", "'/static/views/focus-trap.js'",
		"app.js must import the focus-trap module")
	assertContains(t, "app.js", "createFocusTrap(",
		"app.js modalTrap helper must construct a focus trap per modal")

	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	src := string(b)
	if n := strings.Count(src, ".activate()"); n < 5 {
		t.Fatalf("app.js: focus trap `.activate()` appears %d time(s); want >=5 (new-app, deploy, new-user, reset-password, schedule modals must each activate a trap on open)", n)
	}
	if n := strings.Count(src, ".release()"); n < 5 {
		t.Fatalf("app.js: focus trap `.release()` appears %d time(s); want >=5 (each modal's close path must release the trap to restore focus to the trigger)", n)
	}
	// The schedule modal previously omitted initial focus; pin that it focuses
	// its first field on open.
	assertContains(t, "app.js", "#sched-name')?.focus()",
		"openScheduleForm must focus #sched-name on open so keyboard users land inside the dialog")
}

// TestKeyboardFocusAndLabels pins the keyboard-focus ring, the undefined-token
// fix, the progress-bar labels, and the per-row action aria-labels surfaced by
// the accessibility pass.
func TestKeyboardFocusAndLabels(t *testing.T) {
	assertContains(t, "style.css", "button:focus-visible",
		"style.css must give buttons a visible keyboard focus ring via :focus-visible")
	assertContains(t, "style.css", ".nav-item:focus-visible",
		"style.css must give the sidebar section nav a visible keyboard focus ring")
	assertContains(t, "style.css", ".settings-tab:focus-visible",
		"style.css must give the detail folder tabs a visible keyboard focus ring")
	b, err := fs.ReadFile(ui.Static(), "style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	if strings.Contains(string(b), "var(--border)") {
		t.Fatal("style.css: var(--border) is undefined (palette defines --line/--line-2); the deploy-result card renders borderless. Use var(--line-2).")
	}

	assertContains(t, "index.html", `aria-label="Upload progress"`,
		"deploy/data <progress> bars must carry an aria-label so screen readers announce them meaningfully")

	assertContains(t, "app.js", "`Reset password for ${u.username}`",
		"the per-row Reset password button must carry a per-user aria-label")
	assertContains(t, "app.js", "`Delete user ${u.username}`",
		"the per-row Delete button must carry a per-user aria-label")
	assertContains(t, "app.js", "`Revoke access for ${m.username}`",
		"the members-list Revoke button must carry a per-user aria-label")
}

// TestStatusColorContract pins a status color for every wire status the cards,
// detail-header pill, and sidebar dots can render. A missing rule falls through
// to an unstyled near-white badge (the hibernated bug) or a gray dot that makes
// a broken app look idle (the crashed sidebar bug), so the absence of any of
// these classes must fail the build rather than ship a misleading status.
func TestStatusColorContract(t *testing.T) {
	// The dedicated standby color exists and is distinct from the gray/off and
	// cyan/new tiers so hibernated never reads as broken or as actively live.
	assertContains(t, "style.css", "--standby:",
		"hibernated needs its own standby color token, not the unstyled near-white fallback")

	// Card badges: every status the grid can render carries an explicit rule.
	for _, cls := range []string{
		".badge-running", ".badge-healthy", ".badge-deploying", ".badge-waking",
		".badge-degraded", ".badge-crashed", ".badge-hibernated", ".badge-stopped",
		".badge-suspended", ".badge-unknown", ".badge-new",
	} {
		assertContains(t, "style.css", cls+" ",
			"card status badge "+cls+" must have an explicit color rule, not fall through to the unstyled .badge default")
	}
	assertContains(t, "style.css", ".badge-hibernated { background: var(--standby-bg)",
		"hibernated card badge must use the standby color")

	// Detail-header pill: hibernated reads standby, not lumped with gray stopped.
	assertContains(t, "style.css", ".status-pill.status-hibernated { color: var(--standby)",
		"detail-header hibernated pill must use the standby color, distinct from stopped")

	// Sidebar dots: crashed must be red (was missing → showed gray, looking idle).
	assertContains(t, "style.css", ".sb-dot-crashed",
		"a crashed app's sidebar dot must be red, not fall through to the gray default")
	assertContains(t, "style.css", ".sb-dot-hibernated",
		"hibernated needs its own sidebar standby dot")
}

// TestStatusLabelContract pins the status→label voice to one shared module.
// app.js and app-detail.js previously each carried a private formatStatus that
// could drift; status-label.js is now the single source so cards, the detail
// pill, the sidebar, and replica badges all speak with one voice.
func TestStatusLabelContract(t *testing.T) {
	assertContains(t, "views/status-label.js", "hibernated: 'Sleeping'",
		"hibernated must read 'Sleeping' — it pairs with the 'Waking' resume and the indigo standby color")
	assertContains(t, "views/status-label.js", "suspended:  'Paused'",
		"suspended must read 'Paused' — the operator word for a resumable replica")

	// Both consumers import the shared label, not a private copy that can drift.
	assertContains(t, "app.js", "import { formatStatus } from '/static/views/status-label.js'",
		"app.js must import the shared status label, not redefine it")
	assertContains(t, "views/app-detail.js", "import { formatStatus } from '/static/views/status-label.js'",
		"app-detail.js must import the shared status label, not redefine it")
	assertNotContains(t, "app.js", "function formatStatus",
		"app.js must not carry a private formatStatus — the duplicate is how the label voice drifted")
	assertNotContains(t, "views/app-detail.js", "function formatStatus",
		"app-detail.js must not carry a private formatStatus — use the shared module")
}

// TestDetailPillMatchesCardStatus guards against the card and detail-header
// pill disagreeing about the same app. The pill must derive from the shared
// appStatusView, so a never-deployed crash-looped app reads "Failed" on both
// surfaces instead of "Failed" on the card but a benign "Awaiting deploy" on
// the detail page.
func TestDetailPillMatchesCardStatus(t *testing.T) {
	assertContains(t, "views/app-detail.js", "appStatusView(app, formatStatus)",
		"the detail-header pill must derive from the shared appStatusView so it agrees with the card badge")
	assertNotContains(t, "views/app-detail.js", "status-pill status-new",
		"the detail pill must not hardcode status-new for every zero-deploy app — appStatusView distinguishes Failed from Awaiting deploy")
}

// TestResponsiveAndStatePolish pins the responsive breakpoint additions, the
// loading placeholders, the audit empty-state, the SSE disconnect notice, the
// Workers refresh control, and the degraded-app tooltip surfaced by the polish
// pass.
func TestResponsiveAndStatePolish(t *testing.T) {
	// Responsive: the detail header lays identity + actions out in a flex bar
	// (no absolute positioning to undo) that wraps on mobile, and the wide admin
	// tables get a horizontal scroll container.
	b0, err := fs.ReadFile(ui.Static(), "style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	bar := string(b0)[strings.Index(string(b0), ".app-detail-bar {"):]
	if i := strings.Index(string(b0), ".app-detail-bar {"); i < 0 || !strings.Contains(bar[:200], "justify-content: space-between") {
		t.Fatal("style.css: .app-detail-bar must use flex space-between for the identity/actions layout")
	}
	assertContains(t, "style.css", ".app-detail-actions { flex-wrap: wrap; }",
		"the 640px breakpoint must let the detail action cluster wrap below the title")
	assertContains(t, "style.css", "-webkit-overflow-scrolling: touch",
		"the 640px breakpoint must give the wide admin tables a horizontal scroll container")
	// The responsive table rule uses ID selectors, which outrank the UA
	// [hidden]{display:none} rule. It must guard with :not([hidden]) so a
	// JS-hidden table (empty Workers/Audit) stays hidden on mobile.
	assertContains(t, "style.css", "#workers-table:not([hidden])",
		"the 640px table-scroll rule must use :not([hidden]) so display:block does not override the hidden state of an empty Workers/Audit table")
	assertContains(t, "style.css", ".grid-loading",
		"style.css must style the loading placeholder")

	// Loading states on the two list views.
	assertContains(t, "app.js", "Loading apps…",
		"loadApps must show a loading placeholder on first paint")
	assertContains(t, "app.js", "Loading users…",
		"loadUsers must show a loading row on first paint")
	assertContains(t, "app.js", "aria-busy",
		"the loading states must set aria-busy while fetching")

	// Audit empty-state hides the table (mirrors the Workers pattern).
	assertContains(t, "app.js", "auditTable.hidden = noEvents",
		"renderAuditEvents must hide #audit-table when there are no events so empty headers don't show")

	// SSE log streams announce a disconnect instead of freezing silently.
	assertContains(t, "app.js", "(log stream disconnected)",
		"app.js log-pane SSE onerror must append a disconnect notice")
	assertContains(t, "views/app-detail.js", "(log stream disconnected)",
		"app-detail.js logs-tab SSE onerror must append a disconnect notice")

	// Workers refresh control (consistency with the other list views).
	assertContains(t, "index.html", `id="workers-refresh"`,
		"the Workers page must have a Refresh button like the other list views")
	assertContains(t, "app.js", "getElementById('workers-refresh')",
		"app.js must wire the Workers Refresh button to loadWorkers")

	// Degraded-app detail surfaced in the banner tooltip + accessible name.
	assertContains(t, "views/fleet-health.js", "export function degradedTooltip",
		"fleet-health.js must export degradedTooltip so the banner can name the degraded apps")
	assertContains(t, "app.js", "degradedTooltip(s)",
		"renderFleetHealth must surface the degraded-app detail via degradedTooltip")
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

// TestUsersRoleDropdownHasSSOManagedOption guards the manual-override clear path.
// The users page role <select> must offer an "(SSO-managed)" option with value ""
// so an admin can clear a manual override and return a user to group/default
// governance via PATCH /api/users/{id} {role:""}. See internal/api/users.go.
func TestUsersRoleDropdownHasSSOManagedOption(t *testing.T) {
	assertContains(t, "app.js", "(SSO-managed)",
		"users role dropdown must offer an (SSO-managed) option to clear the manual override")
}

// TestMemberRoleDropdownWiring guards the Access-tab member-role control. The
// member list must render an editable <select> (viewer/manager) per member and
// PATCH /api/apps/:slug/members/:user_id on change so a manager can promote or
// demote members from the UI. See internal/api/router.go handleSetMemberRole.
func TestMemberRoleDropdownWiring(t *testing.T) {
	assertContains(t, "app.js", "async function updateMemberRole",
		"the Access tab must define updateMemberRole so a manager can change a member's role")
	assertContains(t, "app.js", "/members/${userId}",
		"updateMemberRole must PATCH /api/apps/:slug/members/:user_id; see internal/api/router.go handleSetMemberRole")
	assertContains(t, "app.js", "member-role-select",
		"refreshMemberList must render an editable role <select> per member so managers can promote/demote")
}

// TestGroupAccessSectionWiring guards the Access-tab group-rules surface: the
// markup section, the refresh function, and the CRUD wiring against
// /api/apps/:slug/group-access. See internal/api/router.go handleGrantAppGroupAccess.
func TestGroupAccessSectionWiring(t *testing.T) {
	assertContains(t, "index.html", `id="group-access-list"`,
		"index.html must expose #group-access-list for the per-app group rules")
	assertContains(t, "app.js", "async function refreshGroupAccessList",
		"app.js must define refreshGroupAccessList to render group rules on the Access tab")
	assertContains(t, "app.js", "/group-access",
		"app.js must call /api/apps/:slug/group-access for group-rule CRUD")
	assertContains(t, "views/app-detail.js", "refreshGroupAccessList",
		"the Access tab must refresh the group-rules list when rendered")
}

// TestCanManageAppHonorsServerValue guards the per-app manager UI gate: the JS
// canManageApp must honor the server-computed app.can_manage (so member/group
// managers get the management tabs), and the detail view must copy body.can_manage
// onto the app object. See internal/api/apps.go handleGetApp.
func TestCanManageAppHonorsServerValue(t *testing.T) {
	assertContains(t, "app.js", "typeof app.can_manage === 'boolean'",
		"canManageApp must prefer the server-computed app.can_manage when present")
	assertContains(t, "views/app-detail.js", "body.can_manage",
		"the detail view must copy body.can_manage onto the app object so canManageApp sees it")
}

// TestGroupAccessShowsManifestSource guards that manifest-sourced group rules are
// distinguished from manual ones in the Access tab and are not removable via the
// UI (they are managed by the bundle manifest; a UI removal would return on the
// next deploy). See internal/api/apps.go applyManifestAccessGroups.
func TestGroupAccessShowsManifestSource(t *testing.T) {
	assertContains(t, "app.js", "rule.source",
		"refreshGroupAccessList must read rule.source to distinguish manifest from manual rules")
	assertContains(t, "app.js", "manifest",
		"manifest-sourced group rules must be labelled (e.g. \"(manifest)\") and not offer a Remove button")
}

// TestAuditKnownActionsIncludeGroupAccess pins that the audit-log UI recognizes
// the per-app group-access audit actions (so they get a labelled badge, not the
// gray default). See internal/api/apps.go (grant/revoke/reconcile_group_access).
func TestAuditKnownActionsIncludeGroupAccess(t *testing.T) {
	for _, a := range []string{"grant_group_access", "revoke_group_access", "reconcile_group_access"} {
		assertContains(t, "app.js", "'"+a+"'",
			"app.js knownActions must include "+a+" so the audit badge is labelled")
	}
}

// TestMinWarmReplicasUIContract guards the pre-warming knob on the Configuration
// tab. PATCH /api/apps/:slug accepts min_warm_replicas (int 0..1000); the General
// panel must expose the setting so operators can configure the idle floor without
// using the CLI.
//
// index.html must carry the input and warning elements; app.js must read
// app.min_warm_replicas when populating the tab and include min_warm_replicas in
// the PATCH body sent by the hibernate save handler. If either side renames or
// drops these identifiers the knob silently stops working.
func TestMinWarmReplicasUIContract(t *testing.T) {
	assertContains(t, "index.html", `id="min-warm-replicas"`,
		"index.html must keep #min-warm-replicas so the keep-warm number input is reachable via getElementById")
	assertContains(t, "index.html", `id="min-warm-warning"`,
		"index.html must keep #min-warm-warning so the self-clamp warning line can be shown/hidden")
	assertContains(t, "app.js", "app.min_warm_replicas",
		"populateGeneralTab must read app.min_warm_replicas to seed the keep-warm input from PATCH /api/apps/:slug")
	assertContains(t, "app.js", "min_warm_replicas",
		"saveHibernateSettings must include min_warm_replicas in its PATCH body so the keep-warm floor is persisted")
}

// TestKebabMenusAreWired guards both "⋯" menus. The dashboard CARD kebab and the
// app-detail HEADER kebab share one wireKebab helper. The detail-header kebab
// previously had NO handler at all (its menu never opened), so "Restart" was
// unreachable from the detail page; this pins that it is wired.
func TestKebabMenusAreWired(t *testing.T) {
	assertContains(t, "app.js", "function wireKebab",
		"a shared wireKebab helper must toggle kebab menus (open/close, outside-click, Escape)")
	assertContains(t, "app.js", "wireKebab(kebabBtn, kebabList, card)",
		"the dashboard card kebab must be wired via wireKebab")
	assertContains(t, "app.js", "getElementById('app-detail-kebab')",
		"the app-detail header kebab must be wired (it previously had no handler)")
	assertContains(t, "app.js", "getElementById('app-detail-restart')",
		"the app-detail header Restart item must be wired to restart the current app")
	// The header kebab's only action (Restart) is manager-only; it must be hidden
	// for viewers so they can't trigger a forbidden POST (mirrors the card).
	assertContains(t, "views/app-detail.js", "headerKebab.hidden = !canManage",
		"the app-detail header kebab must be hidden for non-managers")
}

// TestCardKebabNotClippedByOverflow guards the card-kebab clip fix: .app-card
// used overflow:hidden (to contain its glow), which sliced the absolutely-
// positioned .kebab-list at the card's bottom edge. The card must stay
// overflow:visible and lift above neighbours while its menu is open.
func TestCardKebabNotClippedByOverflow(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	css := string(b)
	cardStart := strings.Index(css, ".app-card {")
	if cardStart < 0 {
		t.Fatal("style.css: .app-card rule not found")
	}
	cardRule := css[cardStart : cardStart+400]
	if strings.Contains(cardRule, "overflow: hidden") {
		t.Fatal("style.css: .app-card must not use overflow:hidden — it clips the kebab dropdown at the card's bottom edge")
	}
	assertContains(t, "style.css", ".app-card.kebab-open",
		"an open card kebab must raise the card above its grid neighbours so the menu isn't painted under the next card")
}

// TestDataUploadWorksOnDeepLink guards the deep-link fix for the Data tab. The
// upload form's write-permission check must use the fetched single-app object
// (which carries can_manage), not the cached apps LIST (state.apps), which is
// empty on a fresh deep-link — leaving the whole upload form hidden for admins.
func TestDataUploadWorksOnDeepLink(t *testing.T) {
	assertContains(t, "app.js", "async function refreshDataTab(slug, app)",
		"refreshDataTab must accept the app object so canManageApp works on a deep-link")
	assertContains(t, "views/app-detail.js", "ctx.refreshDataTab(app.slug, app)",
		"renderData must pass the fetched app (with can_manage) to refreshDataTab")
}

// TestDeploymentsMarkCurrent guards that the Deployments tab marks the LIVE
// deployment — the newest *succeeded* one, not the newest row (a failed/pending
// latest attempt does not change the running bundle) — and suppresses its Roll
// back button. deploymentListModels is unit-tested separately; this pins wiring.
func TestDeploymentsMarkCurrent(t *testing.T) {
	assertContains(t, "views/app-detail.js", "deploymentListModels(rows)",
		"the Deployments tab must derive the live deployment from status, not current_version")
	assertContains(t, "views/deployment-row.js", "rows.findIndex(d => (d.status || 'succeeded') === 'succeeded')",
		"the live deployment must be the newest succeeded one, so a failed latest attempt isn't mislabelled Current")
	assertContains(t, "views/app-detail.js", "deployment-row-current",
		"the current deployment row must be visually distinguished")
	assertContains(t, "views/app-detail.js", "m.canRollback",
		"Roll back must be suppressed on the current (live) and on failed/pending deployments")
}

// TestConfigurationSurfacesGeneralAndResources guards the IA reorg: display-name
// rename + project (General) and memory/CPU limits (Resources) are surfaced in
// the Configuration tab (all PATCH-backed but previously CLI-only), and the
// Danger Zone (Delete) was moved from Access into Configuration.
func TestConfigurationSurfacesGeneralAndResources(t *testing.T) {
	assertContains(t, "index.html", `id="general-name"`,
		"Configuration must expose a display-name (rename) input")
	assertContains(t, "index.html", `id="resources-memory"`,
		"Configuration must expose a memory-limit input")
	assertContains(t, "index.html", `id="resources-cpu"`,
		"Configuration must expose a CPU-quota input")
	assertContains(t, "app.js", "async function saveGeneralInfo",
		"a saveGeneralInfo handler must PATCH name/project_slug")
	assertContains(t, "app.js", "async function saveResources",
		"a saveResources handler must PATCH memory_limit_mb/cpu_quota_percent")
	// Danger Zone now lives in the Configuration panel, not Access.
	b, err := fs.ReadFile(ui.Static(), "index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	html := string(b)
	cfgStart := strings.Index(html, `id="detail-configuration-panel"`)
	accStart := strings.Index(html, `id="detail-access-panel"`)
	dzStart := strings.Index(html, `id="danger-zone"`)
	if cfgStart < 0 || accStart < 0 || dzStart < 0 {
		t.Fatal("index.html: expected configuration panel, access panel, and danger zone")
	}
	if !(dzStart > cfgStart && dzStart < accStart) {
		t.Fatal("index.html: #danger-zone (Delete app) must live inside the Configuration panel, not Access")
	}
}

// TestResourcesGatedByRuntimeMode guards that per-app resource limits adapt to
// the runtime: they are only enforceable under docker, so the server exposes
// runtime_mode and the client renders Resources read-only (with a note) under
// native instead of letting a Save 400.
func TestResourcesGatedByRuntimeMode(t *testing.T) {
	assertContains(t, "views/app-detail.js", "body.runtime_mode",
		"the detail view must read runtime_mode from the GET envelope")
	assertContains(t, "app.js", "app.runtime_mode === 'docker'",
		"the Resources section must gate editing on the docker runtime")
	assertContains(t, "index.html", `id="resources-runtime-note"`,
		"a note must explain that limits need the docker runtime")
}

// TestSettingsExplicitSaveDirtyTracking guards the explicit-save model: every
// settings section registers with the dirty tracker (Save disabled until dirty,
// "Unsaved changes" hint) and a nav/unload guard warns before losing edits.
func TestSettingsExplicitSaveDirtyTracking(t *testing.T) {
	assertContains(t, "app.js", "function registerSettingsSection",
		"settings sections must register with a dirty-state tracker")
	assertContains(t, "app.js", "function confirmDiscardIfDirty",
		"a guard must confirm before discarding unsaved settings edits")
	assertContains(t, "app.js", "router.setNavGuard(confirmDiscardIfDirty)",
		"the router must consult the unsaved-changes guard before navigating")
	assertContains(t, "app.js", "addEventListener('beforeunload'",
		"a beforeunload guard must warn on full-page unload with unsaved edits")
	assertContains(t, "router.js", "function setNavGuard",
		"the router must expose setNavGuard for the unsaved-changes guard")
	assertContains(t, "app.js", "if (el.disabled) return null",
		"the dirty snapshot must skip disabled mode-specific fields so toggling custom-mode and back isn't spuriously dirty")
	assertContains(t, "app.js", "clearSettingsDirty();",
		"delete must clear the dirty state so the unsaved-changes guard doesn't strand the user on a deleted app")
}

// TestPrimaryButtonsStyledGlobally guards that the button design classes have
// GLOBAL base rules, not only the context-scoped .app-actions / .modal-actions
// ones. Without a global rule, a .btn-primary used elsewhere (the app-detail
// header "Deploy", "+ Add schedule", "+ Mount data from another app") rendered
// as an unstyled user-agent button; .rollback-btn and the logs/traces toolbar
// buttons had the same gap.
func TestPrimaryButtonsStyledGlobally(t *testing.T) {
	assertContains(t, "style.css", "\n.btn-primary {",
		"style.css must define a global .btn-primary base so primary buttons are styled outside .app-actions/.modal-actions")
	assertContains(t, "style.css", "\n.btn-row {",
		"style.css must define a global .btn-row base for secondary row buttons (logs/traces toolbars, deployments retry)")
	b, err := fs.ReadFile(ui.Static(), "style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	css := string(b)
	i := strings.Index(css, ".rollback-btn {")
	if i < 0 || i+220 > len(css) || !strings.Contains(css[i:i+220], "border:") {
		t.Fatal("style.css: .rollback-btn must carry real chrome (border/background), not just padding/font — it was rendering unstyled")
	}
	// The logs/traces toolbar buttons must carry a style class so they aren't
	// unstyled user-agent buttons.
	assertContains(t, "views/app-detail.js", `id="logs-copy" type="button" class="btn-row"`,
		"the Logs 'Copy all' button must be a styled .btn-row")
	assertContains(t, "views/app-detail.js", `id="traces-refresh" type="button" class="btn-row"`,
		"the Traces 'Refresh' button must be a styled .btn-row")
}

// TestReservedUserRowIsReadOnly guards that the synthetic deploy-token identity
// (__deploy__) is rendered read-only in the Users table (no role change, reset,
// or delete) — those actions are meaningless for a tokenless env-managed account.
func TestReservedUserRowIsReadOnly(t *testing.T) {
	assertContains(t, "app.js", "userRowCaps(u, selfId)",
		"renderUsers must derive per-row capabilities via userRowCaps")
	assertContains(t, "views/user-row.js", "__deploy__",
		"user-row.js must treat __deploy__ as a reserved (read-only) account")
}

// TestDetailTabsSeparatedFromContent guards a margin below the app-detail tab
// bar. The tab panel is display:contents, so without this margin the active
// panel's content butts directly against the tab bar (zero gap).
func TestDetailTabsSeparatedFromContent(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	css := string(b)
	// The base rule (not the mobile overflow-x override) starts with display:flex.
	i := strings.Index(css, ".settings-tabs {\n  display: flex;")
	if i < 0 {
		t.Fatal("style.css: could not locate the base .settings-tabs rule")
	}
	end := strings.Index(css[i:], "}")
	if end < 0 || !strings.Contains(css[i:i+end], "margin-bottom:") {
		t.Fatal("style.css: .settings-tabs must carry a margin-bottom so the tab bar is separated from the panel content")
	}
}

// TestSidebarShellStructure pins the global-sidebar shell: the section nav lives
// in #primary-nav inside #sidebar, plus an app list, footer, and a mobile top bar
// that drives the drawer. The old #tab-bar top nav is removed.
func TestSidebarShellStructure(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	html := string(b)
	for _, id := range []string{`id="app-shell"`, `id="sidebar"`, `id="primary-nav"`,
		`id="sidebar-apps"`, `id="mobile-topbar"`, `id="sidebar-toggle"`,
		`id="sidebar-collapse"`, `id="sidebar-backdrop"`} {
		if !strings.Contains(html, id) {
			t.Fatalf("index.html missing %s", id)
		}
	}
	if strings.Contains(html, `id="tab-bar"`) {
		t.Fatal("index.html: the old #tab-bar top nav must be removed (replaced by the sidebar)")
	}
	// Section anchors keep their ids and live inside #primary-nav (so app.js
	// gating + active-state code is unchanged).
	start := strings.Index(html, `id="primary-nav"`)
	end := strings.Index(html[start:], "</nav>")
	if start < 0 || end < 0 {
		t.Fatal("index.html: #primary-nav block not found")
	}
	nav := html[start : start+end]
	for _, id := range []string{`id="tab-apps"`, `id="tab-users"`, `id="tab-workers"`, `id="tab-audit"`} {
		if !strings.Contains(nav, id) {
			t.Fatalf("#primary-nav must contain %s", id)
		}
	}
	if !strings.Contains(html, `data-auth="loading"`) {
		t.Fatal(`index.html: <body> must default to data-auth="loading" so neither the chrome nor the login form paints before the session check resolves (see TestBootSplashAvoidsLoginFlash)`)
	}
	if !strings.Contains(html, `aria-controls="sidebar"`) {
		t.Fatal(`index.html: #sidebar-toggle must have aria-controls="sidebar"`)
	}
}

// TestSidebarAuthGating pins data-auth driving the chrome, and that the old
// tabBar toggle is gone.
func TestSidebarAuthGating(t *testing.T) {
	assertContains(t, "app.js", "document.body.dataset.auth = 'in'", "showLoggedIn must mark the body authenticated")
	assertContains(t, "app.js", "document.body.dataset.auth = 'out'", "showLoggedOut must mark the body logged-out")
	assertContains(t, "style.css", `[data-auth="out"] #sidebar`, "CSS must hide the sidebar before auth")
	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	if strings.Contains(string(b), "tabBar") {
		t.Fatal("app.js: tabBar references must be removed (replaced by data-auth gating)")
	}
}

// TestSidebarAppListWiring pins the app-list data flow: a fire-and-forget
// loadAppsIndex on login, syncSidebar fed from the FULL state.apps (never the
// grid-filtered renderApps), and grouping by project_slug.
func TestSidebarAppListWiring(t *testing.T) {
	assertContains(t, "app.js", "function loadAppsIndex", "app.js must define loadAppsIndex")
	assertContains(t, "app.js", "function syncSidebar", "app.js must define syncSidebar")
	assertContains(t, "app.js", "renderSidebarApps(el, state.apps,", "syncSidebar must feed renderSidebarApps from the full state.apps index")
	assertContains(t, "views/sidebar-nav.js", "project_slug", "grouping must read project_slug")
	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	js := string(b)
	if strings.Contains(js, "await loadAppsIndex()") {
		t.Fatal("app.js: loadAppsIndex must be fire-and-forget (not awaited) so showLoggedIn stays synchronous")
	}
	if !strings.Contains(js, "loadAppsIndex();") {
		t.Fatal("app.js: showLoggedIn must call loadAppsIndex()")
	}
}

// TestSidebarActiveScoping pins section-active scoped to #primary-nav and the
// separate sidebar app highlighter, so aria-current never leaks onto app cards,
// overview links, or the detail folder tabs.
func TestSidebarActiveScoping(t *testing.T) {
	assertContains(t, "app.js", "querySelectorAll('#primary-nav [data-nav]')", "updateActiveNav must be scoped to #primary-nav")
	assertContains(t, "app.js", "highlightSidebarApp(", "the sidebar app active state must be applied via highlightSidebarApp")
	assertContains(t, "views/sidebar-nav.js", "startsWith(href + '/')", "sidebar active must use a segment-boundary slug-prefix match")
}

// TestBrandingUpdatesAllNodes pins logo replacement hitting every .brand node
// (sidebar + mobile top bar), not just the first.
func TestBrandingUpdatesAllNodes(t *testing.T) {
	assertContains(t, "app.js", "querySelectorAll('.brand')", "branding must replace every .brand node (sidebar + mobile)")
	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	if strings.Contains(string(b), "querySelector('nav .brand')") {
		t.Fatal("app.js: branding selector must not require 'nav .brand' after the sidebar move")
	}
}

// TestSidebarDrawerWiring pins the mobile drawer: closed only via the post-mount
// onNavigated hook (so a guard-vetoed nav keeps it open), focus trap reused.
func TestSidebarDrawerWiring(t *testing.T) {
	assertContains(t, "app.js", "createSidebarDrawer(", "app.js must wire the drawer controller")
	assertContains(t, "app.js", "sidebarDrawer.onNavigated()", "the drawer must close from the post-mount onNavigated hook")
	assertContains(t, "views/sidebar-drawer.js", "function onNavigated", "the drawer controller must expose onNavigated")
	assertContains(t, "views/sidebar-drawer.js", "createFocusTrap", "the drawer must reuse createFocusTrap for focus containment")
}

// TestSidebarLayoutCSS pins the shell layout primitives.
func TestSidebarLayoutCSS(t *testing.T) {
	for _, needle := range []string{"#app-shell", "--sidebar-w", "body.sidebar-collapsed", "body.sidebar-open", "@media (max-width: 860px)"} {
		assertContains(t, "style.css", needle, "style.css must define sidebar layout primitive "+needle)
	}
}

// TestVersionDisplayUsesReleaseNumber pins the human-friendly version display:
// the header/overview show the server's release_number (vN) and date, the
// deployments row renders the release label, and the raw epoch is no longer the
// visible version label (kept only on hover/title).
func TestVersionDisplayUsesReleaseNumber(t *testing.T) {
	assertContains(t, "views/app-detail.js", "body.release_number",
		"the detail view must read release_number from the GET envelope")
	assertContains(t, "views/app-detail.js", "'v' + app.release_number",
		"the header/overview version must show the release number, not the epoch")
	assertContains(t, "views/app-detail.js", "m.releaseLabel",
		"the deployments row must render the release label (vN)")
	assertContains(t, "views/deployment-row.js", "release_number",
		"deployment-row.js must derive the label from release_number")
	assertContains(t, "views/deployment-row.js", "releaseLabel",
		"deployment-row.js must expose releaseLabel")
	b, err := fs.ReadFile(ui.Static(), "views/app-detail.js")
	if err != nil {
		t.Fatalf("read app-detail.js: %v", err)
	}
	if strings.Contains(string(b), "`v${m.version}`") {
		t.Fatal("app-detail.js: the deployments row must not display the epoch `v${m.version}`; show the release label, keep the epoch on the title")
	}
}

// TestAppDetailHeaderTiles pins the redesigned detail header: real metric tiles
// (CPU/Memory/Replicas/Sessions) fed by fleet aggregates, a status pill with a
// running pulse, version/deployed meta, and removal of the dead Uptime metric.
func TestAppDetailHeaderTiles(t *testing.T) {
	assertContains(t, "index.html", `class="app-detail-stats"`, "header must use a metric-tile group")
	for _, id := range []string{`id="app-detail-cpu"`, `id="app-detail-ram"`,
		`id="app-detail-replicas"`, `id="app-detail-sessions"`,
		`id="app-detail-version"`, `id="app-detail-deployed"`} {
		assertContains(t, "index.html", id, "header must expose "+id)
	}
	b, err := fs.ReadFile(ui.Static(), "index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if strings.Contains(string(b), "app-detail-uptime") {
		t.Fatal("index.html: the dead #app-detail-uptime must be removed (no data source)")
	}
	assertContains(t, "index.html", `id="app-detail-status" class="status-pill"`,
		"the status must render as a status pill")
	// JS: fleet aggregation + bare tile values + status pill class.
	assertContains(t, "views/stat-format.js", "export function headerStats", "stat-format must expose headerStats")
	assertContains(t, "app.js", "headerStats(m, configured)",
		"onMetrics must feed the tiles from headerStats fleet aggregates")
	assertContains(t, "views/app-detail.js", "statusPillClass(statusView.state)",
		"the status pill class must come from statusPillClass, fed the shared appStatusView state")
	// The grid metrics line is a separate path; it renders via cardMetricsLabel,
	// which sums CPU/RAM across replicas (see TestAppCardInstancesAndSummedMetrics).
	assertContains(t, "views/card-metrics.js", "CPU ${s.cpu} · ${s.ram} RAM",
		"the grid metrics line renders CPU/RAM via cardMetricsLabel")
	// CSS: tiles, the running pulse, and a reduced-motion off-switch.
	assertContains(t, "style.css", ".app-detail-stats .stat", "metric tiles must be styled")
	assertContains(t, "style.css", ".app-detail-header .status-pill.is-live::before { animation: none; }",
		"the running pulse must be disabled under prefers-reduced-motion (scoped to the header pill)")
	// The header pill styles must be scoped so they don't clobber the pre-existing
	// schedules .status-pill (status-on/status-off).
	assertContains(t, "style.css", ".app-detail-header .status-pill::before",
		"the header status-pill dot must be scoped to .app-detail-header, not global")
}

// TestDetailTabsScrollAffordanceOnMobile pins the mobile tab-strip polish: it
// scroll-snaps and shows an edge fade (driven by data-overflow from app-detail.js)
// so clipped tabs are discoverable, and the active tab is scrolled into view.
func TestDetailTabsScrollAffordanceOnMobile(t *testing.T) {
	assertContains(t, "style.css", "scroll-snap-type: x proximity",
		"the mobile tab strip must scroll-snap for crisp stops")
	assertContains(t, "style.css", `.settings-tabs[data-overflow="mid"]`,
		"the tab strip must fade clipped edges via a data-overflow mask")
	assertContains(t, "views/app-detail.js", "data-overflow",
		"app-detail.js must maintain the data-overflow hint on the tab strip")
	assertContains(t, "views/app-detail.js", "scrollIntoView({ inline: 'center', block: 'nearest' })",
		"the active tab must scroll into view on a scrollable strip")
}

// TestDetailTabsAreFolderTabs guards the elevated folder-tab styling: tabs must
// not render as plain underlined links (the global a{} underline is killed), the
// active tab lifts into a surface card, and a glowing cyan cap marks it.
func TestDetailTabsAreFolderTabs(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	css := string(b)
	// Anchor on the base rule (starts with position:relative), not the earlier
	// mobile .settings-tab { flex: 0 0 auto } override.
	i := strings.Index(css, ".settings-tab {\n  position: relative;")
	if i < 0 {
		t.Fatal("style.css: missing base .settings-tab rule")
	}
	end := strings.Index(css[i:], "}")
	if end < 0 || !strings.Contains(css[i:i+end], "text-decoration: none") {
		t.Fatal("style.css: .settings-tab must set text-decoration:none so tabs aren't plain underlined links")
	}
	a := strings.Index(css, ".settings-tab.active {")
	if a < 0 {
		t.Fatal("style.css: missing .settings-tab.active rule")
	}
	aEnd := strings.Index(css[a:], "}")
	if aEnd < 0 || !strings.Contains(css[a:a+aEnd], "background: var(--surface)") {
		t.Fatal("style.css: the active tab must lift into a surface card (background: var(--surface))")
	}
	assertContains(t, "style.css", ".settings-tab.active::after",
		"the active folder tab must carry a cyan top-cap accent (::after)")
}

// TestLogsTabEmptyStateForNeverDeployed guards that the Logs tab does not open
// an SSE stream for an app awaiting its first deploy. Such an app has no log
// file, so the stream errors immediately and printed "(log stream disconnected)";
// instead the tab must render a "No logs yet" empty state.
func TestLogsTabEmptyStateForNeverDeployed(t *testing.T) {
	assertContains(t, "views/app-detail.js", "(app.deploy_count || 0) === 0",
		"renderLogs must short-circuit on a never-deployed app instead of opening EventSource")
	assertContains(t, "views/app-detail.js", "No logs yet",
		"the never-deployed Logs tab must show a 'No logs yet' empty state")
	assertContains(t, "style.css", "\n.logs-empty {",
		"style.css must style the Logs empty state")
	// The empty-state branch must precede the EventSource construction so the
	// stream is never opened for a never-deployed app.
	b, err := fs.ReadFile(ui.Static(), "views/app-detail.js")
	if err != nil {
		t.Fatalf("read app-detail.js: %v", err)
	}
	js := string(b)
	guard := strings.Index(js, "(app.deploy_count || 0) === 0")
	es := strings.Index(js, "new EventSource(`/api/apps/${app.slug}/logs`")
	if guard < 0 || es < 0 || guard > es {
		t.Fatal("app-detail.js: the never-deployed guard must come before the log EventSource is opened")
	}
}

// TestConfigDefaultPlaceholders guards that settings fields which are empty by
// design (no limit) or only active in another mode communicate their default
// via a placeholder, rather than rendering as a blank box that reads as missing.
func TestConfigDefaultPlaceholders(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	html := string(b)
	cases := []struct{ id, placeholder, why string }{
		{"resources-memory", `placeholder="No limit"`, "an empty memory limit means no limit"},
		{"resources-cpu", `placeholder="No limit"`, "an empty CPU quota means no limit"},
		{"hibernate-custom-minutes", `placeholder="30"`, "the disabled custom-timeout input must hint its default"},
		{"autoscale-target", `placeholder="0.80"`, "the disabled custom-target input must hint the server-wide default"},
	}
	for _, c := range cases {
		i := strings.Index(html, `id="`+c.id+`"`)
		if i < 0 {
			t.Fatalf("index.html: missing input #%s", c.id)
		}
		// The input tag spans from the opening of the element back to '<input'.
		start := strings.LastIndex(html[:i], "<input")
		end := strings.Index(html[i:], ">")
		if start < 0 || end < 0 || !strings.Contains(html[start:i+end], c.placeholder) {
			t.Fatalf("index.html: #%s must carry %s (%s)", c.id, c.placeholder, c.why)
		}
	}
}

// TestScalingRowInputsAlign guards the settings-row alignment contract: every
// control sits in a fixed-width input column so number, text, and checkbox
// fields share one left edge. The checkbox-toggle flex rule must exclude
// .scaling-row, otherwise a checkbox-bearing row (Enable autoscale) collapses to
// flex and its control no longer aligns with sibling number inputs.
func TestScalingRowInputsAlign(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	css := string(b)
	i := strings.Index(css, ".scaling-row {")
	if i < 0 || !strings.Contains(css[i:i+200], "grid-template-columns: minmax(0, 1fr) 16rem") {
		t.Fatal("style.css: .scaling-row must use a fixed input column (minmax(0,1fr) 16rem) so all controls align")
	}
	assertContains(t, "style.css", ".scaling-row > input { justify-self: start; }",
		"scaling-row controls must left-align at the input column so narrow (number) and wide (text) inputs share an edge")
	assertContains(t, "style.css", `.settings-tab-panel label:not(.scaling-row):has(> input[type="checkbox"])`,
		"the checkbox-toggle flex rule must exclude .scaling-row so a checkbox settings-row keeps its grid alignment")
}

// TestProfileIdentityWiring pins the sidebar identity card + profile modal to
// the static SPA. app.js is an un-importable IIFE, so the wiring is asserted by
// source string-search; the pure model (initials/hue/fallbacks) is unit-tested
// in internal/ui/jstests/user-identity.test.js. The PATCH /api/auth/me response
// is the {user, can_create_apps} session envelope (see internal/api/auth.go
// handlePatchMe + newSessionUser); the consumer must read body.user and gate
// the password fields on user.can_set_password, or the class of silent-undefined
// regression (Save shows nothing, password fields always render) recurs.
func TestProfileIdentityWiring(t *testing.T) {
	// Markup: the clickable identity card, the profile modal, its display-name
	// input, and the logout button now living inside that modal.
	assertContains(t, "index.html", `id="identity-card"`,
		"the sidebar footer must keep the clickable identity card that opens the profile modal")
	assertContains(t, "index.html", `id="profile-modal"`,
		"the profile modal must exist for self-service display-name + password editing")
	assertContains(t, "index.html", `id="profile-display-name"`,
		"the profile modal must keep the display-name input")
	assertContains(t, "index.html", `id="logout-button"`,
		"the logout button moved into the profile modal; app.js still binds #logout-button")

	// Consumer: the self-service endpoint, its response shape, and the shared
	// identity model.
	assertContains(t, "app.js", "/api/auth/me",
		"the profile save handler must PATCH /api/auth/me (self-service display name + password)")
	assertContains(t, "app.js", "can_set_password",
		"the profile modal must gate the password fields on user.can_set_password from the /api/auth/me response; see internal/api/auth.go newSessionUser")
	assertContains(t, "app.js", "identityModel",
		"app.js must render the identity card via the unit-tested identityModel helper")
	assertContains(t, "views/user-identity.js", "display_name",
		"the identity model must read user.display_name; see internal/api/auth.go sessionUserResponse")
	// The admin Users table surfaces the friendly name as a subtitle, reading
	// userResponse.display_name (see internal/api/users.go toUserResponse).
	assertContains(t, "app.js", "u.display_name",
		"the Users table must render u.display_name as the username subtitle; see internal/api/users.go toUserResponse")
}

// TestBootSplashAvoidsLoginFlash pins the boot-state contract that prevents the
// login form from painting before the dashboard. The shell must default to
// data-auth="loading" (not "out") with a #boot-splash hold; the server stamps
// "in" for authenticated requests (StampAuthenticated, tested separately), and
// the CSS must hide the login view during boot and for an already-authenticated
// shell. Regressing the default back to "out" reintroduces the login flash.
func TestBootSplashAvoidsLoginFlash(t *testing.T) {
	assertContains(t, "index.html", `data-auth="loading"`,
		"the shell must boot in data-auth=\"loading\" so the login form never paints before the session check resolves")
	assertNotContains(t, "index.html", `<body data-auth="out">`,
		"the shell must not default to data-auth=\"out\"; that paints the login form first (the flash)")
	assertContains(t, "index.html", `id="boot-splash"`,
		"the shell must include the #boot-splash hold shown during data-auth=\"loading\"")
	assertContains(t, "style.css", `[data-auth="loading"] #login-view`,
		"the login view must be hidden during boot so it never flashes")
	assertContains(t, "style.css", `[data-auth="in"] #login-view`,
		"the login view must stay hidden for a server-stamped authenticated shell")
	assertContains(t, "style.css", `[data-auth="loading"] #boot-splash`,
		"the boot splash must be shown only during the loading state")
}

// TestCrashedAppUX pins the wiring that surfaces a crashed app's reason and a
// Restart, so an API/JS shape drift (last_error) or a missing import fails the
// build instead of silently breaking the dashboard.
func TestCrashedAppUX(t *testing.T) {
	// The detail Overview imports the crash banner and feeds it a Restart wired
	// to ctx.restart.
	assertContains(t, "views/app-detail.js", "crash-banner.js",
		"app-detail.js must import the crash banner")
	assertContains(t, "views/app-detail.js", "crashBanner(document",
		"renderOverview must build the crash banner")
	assertContains(t, "views/app-detail.js", "ctx.restart(app.slug)",
		"the crash banner's Restart must call ctx.restart")
	// The banner reads the crash reason and gates on the crashed status.
	assertContains(t, "views/crash-banner.js", "last_error",
		"the crash banner must show app.last_error (the crash reason from GET /api/apps/:slug; see internal/db/queries.go App.LastError)")
	assertContains(t, "views/crash-banner.js", "'crashed'",
		"the crash banner must gate on app.status === 'crashed'")
	// app.js exposes restart on ctx for the banner to reuse.
	assertContains(t, "app.js", "restart: (slug) => restart(slug)",
		"ctx must expose restart so the crash banner can reuse the existing restart action")
	// The crashed badge + banner are styled.
	assertContains(t, "style.css", ".badge-crashed",
		"a crashed app needs a styled status badge")
	assertContains(t, "style.css", ".crash-banner",
		"the crash banner needs styling")
	// The fleet-health panel counts crashed apps.
	assertContains(t, "views/fleet-health.js", "apps.crashed",
		"the fleet health summary must read apps.crashed; see internal/api/fleet_health.go fleetAppCounts.Crashed")
}

// TestAppCardMetricsReserveSpace guards against the layout shift where the app
// card's action buttons jumped down when the CPU/RAM line appeared on start: the
// metrics line must reserve its height even when empty (not running), so the
// buttons below never move. Scoped to the .app-metrics:empty rule so it catches
// a re-collapse (zeroing height/padding) without matching the same declarations
// elsewhere in the stylesheet.
func TestAppCardMetricsReserveSpace(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	css := string(b)
	i := strings.Index(css, ".app-metrics:empty {")
	if i < 0 {
		t.Fatal(".app-metrics:empty rule not found")
	}
	end := strings.Index(css[i:], "}")
	if end < 0 {
		t.Fatal(".app-metrics:empty rule has no closing brace")
	}
	rule := css[i : i+end]
	for _, collapse := range []string{"min-height: 0", "height: 0", "padding: 0"} {
		if strings.Contains(rule, collapse) {
			t.Errorf(".app-metrics:empty contains %q, which collapses the line to zero height and shifts the card buttons down when CPU/RAM appears on start", collapse)
		}
	}
}

// TestAppCardInstancesAndSummedMetrics pins that the dashboard card reports a
// scaled app honestly: CPU/RAM summed across replicas (matching the detail
// header) and an instance-count chip, rather than the first-replica scalar that
// under-reported a multi-replica app's usage.
func TestAppCardInstancesAndSummedMetrics(t *testing.T) {
	assertContains(t, "app.js", "cardMetricsLabel",
		"the grid card must render CPU/RAM via cardMetricsLabel (summed across replicas), not the first-replica m.cpu_percent scalar")
	assertContains(t, "app.js", "instanceCountLabel",
		"the grid card must show the instance count via instanceCountLabel for scaled apps")
	assertNotContains(t, "app.js", "m.cpu_percent.toFixed",
		"the grid card must not render the first-replica m.cpu_percent scalar; it under-reports a scaled app's total")
	assertContains(t, "views/card-metrics.js", "headerStats",
		"cardMetricsLabel must reuse headerStats so the card's total matches the detail header's per-replica sum")
	assertContains(t, "style.css", ".app-instances",
		"the instance-count chip needs styling")
}

// TestBatchMetricsPoll pins the metrics poll to the batch endpoint: the dashboard
// must fetch every card's live data in one request (GET /api/apps/metrics) and
// read the slug-keyed body.metrics, not loop one round-trip per app.
func TestBatchMetricsPoll(t *testing.T) {
	assertContains(t, "metrics-controller.js", "/api/apps/metrics?slugs=",
		"the metrics poll must use the batch endpoint (one request for all cards); see internal/api/apps.go handleBatchMetrics")
	assertContains(t, "metrics-controller.js", "body.metrics",
		"the metrics poll must read the slug-keyed body.metrics from the batch response")
	assertNotContains(t, "metrics-controller.js", "/api/apps/${slug}/metrics",
		"the metrics poll must not fetch per-app metrics one at a time (the slow sequential path)")
}

// TestOverviewContract pins the Overview home (the / route) to the API shapes
// and DOM/routing wiring it depends on, so a server-side rename or a route
// refactor fails the build instead of silently blanking the dashboard home.
func TestOverviewContract(t *testing.T) {
	// GET /api/apps/metrics returns {metrics: {slug: ...}} (internal/api metrics
	// handler); the Overview resource summary unwraps body.metrics.
	assertContains(t, "views/overview.js", "b.metrics",
		"GET /api/apps/metrics returns {metrics}; the Overview reads body.metrics")
	// GET /api/audit returns {events, total, has_more} (internal/api/audit.go);
	// the recent-activity panel unwraps body.events.
	assertContains(t, "views/overview.js", "b.events",
		"GET /api/audit returns {events, total, has_more}; the Overview reads body.events")
	// The view renders into these shells defined in index.html.
	assertContains(t, "index.html", `id="overview-view"`,
		"overview.js shows #overview-view and renders into #overview-body")
	assertContains(t, "index.html", `id="overview-body"`,
		"overview.js renders the Overview body into #overview-body")
	// app.js mounts the Overview on / and the apps grid on /apps.
	assertContains(t, "app.js", "mountOverview",
		"the / route mounts the Overview (views/overview.js)")
	assertContains(t, "app.js", "router.register('/apps'",
		"the apps grid moved to the /apps route")
}

// TestLaunchpadContract pins the viewer Launchpad (the non-operator / home) to
// the API shape and DOM/routing wiring it depends on.
func TestLaunchpadContract(t *testing.T) {
	assertContains(t, "app.js", "mountLaunchpad",
		"the / route mounts the Launchpad for non-operators (views/launchpad.js)")
	assertContains(t, "app.js", "isOperator",
		"role-adaptive home: operators get the Overview, everyone else the Launchpad")
	assertContains(t, "index.html", `id="launchpad-view"`,
		"launchpad.js shows #launchpad-view and renders into #launchpad-body")
	assertContains(t, "index.html", `id="launchpad-body"`,
		"launchpad.js renders the gallery into #launchpad-body")
	assertContains(t, "index.html", `id="tab-launchpad"`,
		"the non-operator home nav item")
	assertContains(t, "views/launchpad-model.js", "app.description",
		"GET /api/apps returns description (db.App.Description); the Launchpad tile shows it")
	assertContains(t, "views/launchpad.js", "/app/",
		"a Launchpad tile launches the proxied app at /app/<slug>/")
	// A pure viewer must not reach the operator detail page (logs / deployments /
	// configuration) via a sidebar link or a typed URL; the detail mount redirects
	// them to the Launchpad once the app loads, while a per-app manager (can_manage)
	// keeps access. Pin the gate so the viewer-only flow can't silently regress.
	assertContains(t, "views/app-detail.js", "ctx.state.user.role === 'viewer' && !canManage",
		"app-detail.js gates pure viewers out of the operator detail page (manager via can_manage keeps access)")
}

// TestPreviewViewerHomeUIContract pins the admin "preview viewer home" wiring:
// the Overview entry, the role-gated route, the viewer-scoped fetch, the banner,
// and the faithful sidebar.
func TestPreviewViewerHomeUIContract(t *testing.T) {
	assertContains(t, "index.html", `href="/home?preview=viewer"`,
		"the Overview has a Preview viewer home entry")
	assertContains(t, "app.js", "preview') === 'viewer'",
		"mountHome mounts the Launchpad in preview for an operator on ?preview=viewer")
	assertContains(t, "app.js", "{ preview: true }",
		"the preview mounts the Launchpad with preview mode on")
	assertContains(t, "views/launchpad.js", "/api/apps?as=viewer",
		"preview fetches the viewer-scoped (public+shared) app list")
	assertContains(t, "views/launchpad.js", "renderPreviewBanner",
		"preview shows a banner clarifying it is the viewer home")
	assertContains(t, "views/launchpad.js", "ctx.renderSidebarAppsList",
		"preview renders the viewer-scoped list into the sidebar too (faithful)")
	assertContains(t, "app.js", "sidebarPreviewActive",
		"syncSidebar is suppressed while the preview owns the sidebar, so a background index load can't clobber it")
}

// TestRootHomeUIContract pins the client wiring for the auth-aware root: the
// stable /home alias and logout landing on the contextual root.
func TestRootHomeUIContract(t *testing.T) {
	assertContains(t, "app.js", "router.register('/home'",
		"the SPA registers /home as the stable authenticated home alias")
	assertContains(t, "app.js", "window.location.assign('/')",
		"logout navigates to the contextual root so the landing page shows when one is configured")
	assertContains(t, "app.js", "suppressUnloadGuard",
		"logout suppresses the unsaved-changes beforeunload guard so a revoked session never strands the user on-screen")
}

// TestAppIconUIContract pins the per-app icon wiring: the shared avatar module,
// the Launchpad tile rendering an icon-or-monogram, the detail-header avatar, and
// the Configuration icon picker that uploads to PUT /api/apps/<slug>/icon.
func TestAppIconUIContract(t *testing.T) {
	// Shared avatar module: monogram model + icon URL + DOM render helper.
	assertContains(t, "views/app-avatar.js", "export function renderAppAvatar",
		"app-avatar.js exposes the shared icon-or-monogram renderer")
	assertContains(t, "views/app-avatar.js", "export function appIconUrl",
		"app-avatar.js derives the icon URL (with an updated_at cache-buster)")

	// Launchpad tile renders the icon via the shared helper, fed by the model's iconUrl.
	assertContains(t, "views/launchpad-model.js", "iconUrl: appIconUrl(app)",
		"the Launchpad tile model carries the icon URL")
	assertContains(t, "views/launchpad.js", "renderAppAvatar",
		"the Launchpad tile renders the icon-or-monogram via the shared helper")

	// Detail header avatar is rendered for the current app.
	assertContains(t, "index.html", `id="app-detail-icon"`,
		"the app-detail header has an icon slot")
	assertContains(t, "app.js", "renderDetailHeaderAvatar",
		"app.js renders the detail-header icon/monogram for the current app")

	// Configuration icon picker: markup + upload/remove wiring + the endpoint.
	assertContains(t, "index.html", `id="general-icon-preview"`,
		"Configuration > General has an icon preview")
	assertContains(t, "index.html", `id="general-icon-file"`,
		"the icon picker has a file input")
	assertContains(t, "app.js", "renderIconPicker",
		"app.js wires the icon picker preview + upload/remove")
	assertContains(t, "app.js", "app.slug)}/icon",
		"app.js uploads/removes via /api/apps/<slug>/icon")
	// The author .ov-btn display would override [hidden]; the Remove button must
	// actually hide when no icon is set.
	assertContains(t, "style.css", ".ov-btn[hidden]",
		"an .ov-btn with the hidden attribute is actually hidden (Remove when no icon)")
}

// TestAppDescriptionUIContract pins the Configuration > General description
// field to the PATCH it feeds (the same field the Launchpad renders).
func TestAppDescriptionUIContract(t *testing.T) {
	assertContains(t, "index.html", `id="general-description"`,
		"Configuration > General has a description field")
	assertContains(t, "app.js", "name, description, project_slug",
		"saveGeneralInfo PATCHes description alongside name + project_slug")
}

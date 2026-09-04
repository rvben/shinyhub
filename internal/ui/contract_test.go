package ui_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
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
// buttons on the detail page. The unwrap now lives in the pure, unit-tested
// views/app-detail-envelope.js (normalizeAppEnvelope); this test pins that the
// module keeps reading body.app/body.replicas_status AND that app-detail.js
// keeps calling it, so the class of regression can't recur.
func TestAppDetailUnwrapsGetAppResponse(t *testing.T) {
	assertContains(t, "views/app-detail-envelope.js", "body.app",
		"GET /api/apps/:slug returns {app, replicas_status}; normalizeAppEnvelope must unwrap body.app (see internal/api/apps.go handleGetApp)")
	assertContains(t, "views/app-detail-envelope.js", "body.replicas_status",
		"normalizeAppEnvelope must read body.replicas_status; the Overview Replicas panel seeds from it")
	assertContains(t, "views/app-detail.js", "normalizeAppEnvelope",
		"app-detail.js must call normalizeAppEnvelope to unwrap the GET /api/apps/:slug envelope")
}

// TestAppDetailAccessResolverWired pins that app-detail.js delegates its tab
// resolution + access redirects + tablist visibility to the pure, unit-tested
// views/app-detail-nav.js (resolveDetailAccess, tabViewModels) rather than
// re-inlining the decisions in the untestable mount closure. The behaviour is
// covered by internal/ui/jstests/app-detail-nav.test.js.
func TestAppDetailAccessResolverWired(t *testing.T) {
	assertContains(t, "views/app-detail.js", "resolveDetailAccess",
		"app-detail.js must resolve the tab + access redirects via resolveDetailAccess (viewer -> '/apps', non-manager off manager-only tabs)")
	assertContains(t, "views/app-detail.js", "tabViewModels",
		"app-detail.js must build the tab strip visibility/href/roving-tabindex model via tabViewModels")
}

// TestConnectivityBannerWired pins the API/frontend contract for the WebSocket
// health warning. handleGetApp emits envelope.connectivity.serving_without_ws
// (see internal/api/apps.go) when a running app is serving pages but no
// WebSocket has connected; the pure, unit-tested views/connectivity-banner.js
// (jstests/connectivity-banner.test.js) renders the amber warning from it, and
// app-detail.js must import and call it. This keeps the envelope key, the
// consumer, and the wiring from drifting apart into a silently-undefined field.
func TestConnectivityBannerWired(t *testing.T) {
	assertContains(t, "views/connectivity-banner.js", "connectivity",
		"connectivity-banner.js must read envelope.connectivity (emitted by handleGetApp in internal/api/apps.go)")
	assertContains(t, "views/connectivity-banner.js", "serving_without_ws",
		"connectivity-banner.js must key on connectivity.serving_without_ws to decide whether to warn")
	assertContains(t, "views/app-detail.js", "connectivityBanner",
		"app-detail.js must call connectivityBanner so a WebSocket-blocked app surfaces a warning on the Overview")
}

// TestDeployingBadgeWired pins the API/frontend contract for the card's
// "Deploying" badge. The server computes `deploying` (pending deployment row
// + held deploy lock; see api.Server.appDeploying) onto the apps-list payload
// and the batch-metrics payload (metricsResponse.Deploying). The pure,
// unit-tested views/app-card-badge.js (jstests/app-card-badge.test.js) ranks
// it above every other state, and app.js must forward m.deploying from the
// 10s poll into updateCardStatusBadge so the badge flips live. The
// badge-deploying class must exist in style.css or the state renders
// unstyled.
func TestDeployingBadgeWired(t *testing.T) {
	assertContains(t, "views/app-card-badge.js", "app.deploying",
		"app-card-badge.js must rank the server-computed deploying flag above every other state")
	assertContains(t, "app.js", "deploying: m.deploying",
		"app.js must forward m.deploying from the /metrics poll into updateCardStatusBadge (see metricsResponse.Deploying in internal/api/apps.go)")
	assertContains(t, "app.js", "last_deployment_status: m.last_deployment_status",
		"app.js must forward m.last_deployment_status so a watched failed first deploy renders Failed, not Awaiting deploy")
	assertContains(t, "app.js", "updateStatusPill(pillEl",
		"app.js must live-update the detail-header pill from the /metrics poll so an open detail page flips to Deploying and back")
	assertContains(t, "style.css", ".badge-deploying",
		"style.css must style the badge-deploying state the card badge emits")
	assertContains(t, "style.css", ".status-deploying",
		"style.css must style the status-deploying pill state the detail header emits")
}

// TestLoginProvidersGated pins that SSO login buttons only appear for providers
// the server reports configured via GET /api/auth/providers ({github, google,
// oidc:{enabled,display_name}}; see internal/api/oidc_handler.go handleGetProviders).
// The GitHub and Google buttons are static markup in index.html and were shown
// unconditionally, so a native-only install advertised buttons whose endpoints
// return 501. They are now hidden by default and revealed by the pure,
// unit-tested views/login-providers.js (jstests/login-providers.test.js), which
// app.js must call. The CSS [hidden] override is load-bearing: the button
// display:flex outranks the UA [hidden] rule.
func TestLoginProvidersGated(t *testing.T) {
	assertContains(t, "index.html", `class="github-login" href="/api/auth/github/login" hidden`,
		"the GitHub button must be hidden by default so an unconfigured install never shows a dead 501 button")
	assertContains(t, "index.html", `class="google-login" href="/api/auth/google/login" hidden`,
		"the Google button must be hidden by default so an unconfigured install never shows a dead 501 button")
	assertContains(t, "views/login-providers.js", "p.github === true",
		"providerVisibility must gate the GitHub button on a strict boolean github flag from /api/auth/providers")
	assertContains(t, "views/login-providers.js", "oidc.enabled === true",
		"providerVisibility must gate the OIDC button on oidc.enabled from /api/auth/providers")
	assertContains(t, "app.js", "applyLoginProviders(document",
		"loadProviders must delegate button gating to applyLoginProviders so the wiring stays covered")
	assertContains(t, "style.css", ".github-login[hidden]",
		"style.css must override the button display:flex with a [hidden] rule, or the hidden attribute won't hide it (see .nav-item[hidden])")

	// SSO-only: the password form is hidden when /api/auth/providers reports
	// local:false (handleGetProviders emits it from cfg.Auth.LocalLoginEnabled).
	// The server also rejects password logins with 403 when disabled, so this is
	// a UI convenience, not the security boundary.
	assertContains(t, "views/login-providers.js", "p.local !== false",
		"the password form must be gated on the providers 'local' flag (fail open: only an explicit false hides it)")
	assertContains(t, "views/login-providers.js", "#login-form",
		"login-providers.js must hide #login-form when local login is disabled")
	assertContains(t, "style.css", ".login-box form[hidden]",
		"style.css must override the form display:grid with a [hidden] rule so the SSO-only hidden form is actually hidden")
}

// TestLoginBrandSlot pins the login card's brand slot. Signed out, the sidebar
// and top bar are display:none, so before this the front door carried no
// identity at all: no ShinyHub mark by default, and an operator's configured
// branding.logo reached every slot EXCEPT the only one an anonymous visitor
// sees. The pure swap logic is unit-tested (jstests/branding.test.js); the
// markup and wiring live in index.html and the app.js IIFE, so pin them here.
func TestLoginBrandSlot(t *testing.T) {
	assertContains(t, "index.html", `<div class="login-brand">`,
		"the login card must carry a brand slot, or the signed-out page shows no identity (sidebar/top bar are display:none when out)")
	assertContains(t, "index.html", `<span class="brand-art" aria-hidden="true"></span>`,
		"the stock brand slots must render the theme-aware Orbit Hub artwork")
	assertContains(t, "index.html", `<span class="sr-only">ShinyHub</span>`,
		"the image-based stock lockup must keep an accessible product name")
	assertContains(t, "app.js", "applyBranding(document",
		"app.js must delegate branding to views/branding.js so the swap stays covered by jstests/branding.test.js")
	assertContains(t, "views/branding.js", `doc.querySelectorAll('.brand')`,
		"applyBranding must brand every .brand slot, including the login card")
	assertContains(t, "style.css", ".login-brand .brand-logo",
		"style.css must size an operator logo for the login card, or a full-size asset blows out the box")
	assertContains(t, "style.css", `[data-auth="out"] #login-view`,
		"the signed-out login view must be centred in the viewport, not hung from the top")
}

func TestStockBrandAssetSet(t *testing.T) {
	for _, name := range []string{
		"brand/orbit-hub-lockup-light.svg",
		"brand/orbit-hub-lockup-dark.svg",
		"brand/orbit-hub-mark-light.svg",
		"brand/orbit-hub-mark-dark.svg",
		"brand/orbit-hub-lockup-light.png",
		"brand/orbit-hub-lockup-dark.png",
		"brand/orbit-hub-mark-light.png",
		"brand/orbit-hub-mark-dark.png",
		"brand/favicon-light-16.png",
		"brand/favicon-dark-16.png",
		"brand/favicon-light-32.png",
		"brand/favicon-dark-32.png",
		"brand/favicon-64.png",
		"brand/favicon.ico",
		"brand/apple-touch-icon.png",
	} {
		if _, err := fs.Stat(ui.Static(), name); err != nil {
			t.Errorf("stock brand asset %q must be embedded: %v", name, err)
		}
	}
	for _, name := range []string{
		"brand/orbit-hub-lockup-light.svg",
		"brand/orbit-hub-lockup-dark.svg",
		"brand/orbit-hub-mark-light.svg",
		"brand/orbit-hub-mark-dark.svg",
	} {
		asset, err := fs.ReadFile(ui.Static(), name)
		if err != nil {
			t.Fatalf("read stock vector brand asset %q: %v", name, err)
		}
		source := string(asset)
		if !strings.Contains(source, "<path") {
			t.Errorf("stock vector brand asset %q must contain outlined paths", name)
		}
		for _, forbidden := range []string{"<image", "data:image", "<text", "font-family"} {
			if strings.Contains(source, forbidden) {
				t.Errorf("stock vector brand asset %q must not contain %q", name, forbidden)
			}
		}
	}
	assertContains(t, "index.html", `data-stock-icon`,
		"stock favicon links must be identifiable so a favicon override can replace the complete set")
	assertContains(t, "style.css", `--stock-brand-lockup: url('/static/brand/orbit-hub-lockup-dark.svg')`,
		"dark mode must select the purpose-built dark Orbit Hub lockup")
	assertContains(t, "style.css", `--stock-brand-lockup: url('/static/brand/orbit-hub-lockup-light.svg')`,
		"light mode must select the light Orbit Hub lockup")
	assertContains(t, "style.css", `background-image: var(--stock-brand-mark)`,
		"the collapsed sidebar must use the compact mark rather than shrinking the full wordmark")
}

// TestAboutDialog pins the one surface that tells a signed-in operator which
// ShinyHub they run and what the host can start. Nothing else in the UI reports
// either: the other "version" fields are per-deployment app versions, and
// runtime availability has no other display at all. It lives behind the login
// and behind a click on purpose (the anonymous front door stays the operator's
// brand), and nothing in it may be a .brand slot, or views/branding.js would
// white-label away the software name a bug report needs. The rendering rules
// are unit-tested (jstests/about.test.js); pin markup and wiring here.
func TestAboutDialog(t *testing.T) {
	assertContains(t, "index.html", `<div id="about-modal" class="modal-overlay" hidden`,
		"index.html must carry the About dialog")
	assertContains(t, "index.html", `<p class="about-wordmark" role="img" aria-label="ShinyHub">`,
		"the product identity must be static accessible markup with no .brand class, so branding.js cannot swap the software name away")
	assertContains(t, "index.html", `<p id="about-version" class="about-version"></p>`,
		"the dialog must carry the version slot")
	assertContains(t, "index.html", `<dl id="about-runtimes" class="about-runtimes"></dl>`,
		"the dialog must carry the runtimes slot; /api/server-info reports them and no other view does")
	assertContains(t, "index.html", `<button id="about-button" type="button" class="sidebar-about"`,
		"the sidebar footer must carry the About trigger, or the dialog is unreachable")
	assertContains(t, "index.html", `<span class="nav-label">About ShinyHub</span>`,
		"the visible About trigger must name ShinyHub after external auth bypasses the built-in login")
	assertContains(t, "app.js", "renderAbout(document",
		"app.js must render the dialog via views/about.js so the label rules stay covered by jstests/about.test.js")
	assertContains(t, "app.js", "createServerInfoLoader(",
		"app.js must fetch server info through the caching loader, not a bare per-open fetch")
	assertContains(t, "app.js", "aboutButton.addEventListener('click', openAboutModal)",
		"the About trigger must be wired, or the button is inert")
	assertContains(t, "app.js", "closeAboutModal();",
		"Escape and the close button must dismiss the dialog like every other modal")
	assertContains(t, "views/about.js", "/api/server-info",
		"the loader must read the unauthenticated server-info endpoint")
	assertContains(t, "views/about.js", "'Version unavailable'",
		"an unreachable server must say the version is unknown, never render a blank that reads as 'no version'")
	assertContains(t, "views/about.js", "unknown: 'Unknown'",
		"a runtime the server never reported must read as unknown, never as 'Not installed'")
	assertContains(t, "style.css", ".about-runtimes {",
		"style.css must lay out the runtime rows, or the definition list falls back to browser defaults")
	assertContains(t, "style.css", ".sidebar-about {",
		"style.css must style the About trigger to match the other rail controls")
}

// TestThemeWiring guards the light/dark theme feature. The pure resolver is
// unit-tested (jstests/theme.test.js); the wiring lives in the app.js IIFE and
// index.html, so pin the load-bearing pieces by string search:
//   - a no-flash inline script sets data-theme before first paint,
//   - app.js initialises the theme and handles the Appearance radios,
//   - the palette has a light override keyed on [data-theme="light"],
//   - the profile modal exposes the theme picker.
func TestThemeWiring(t *testing.T) {
	assertContains(t, "index.html", "document.documentElement.dataset.theme",
		"index.html must set data-theme before first paint (no-flash inline script)")
	assertContains(t, "index.html", "shinyhub-theme",
		"the inline theme script must read the persisted preference key")
	assertContains(t, "index.html", `name="theme-pref"`,
		"the profile modal must expose the System/Light/Dark theme picker radios")
	assertContains(t, "app.js", "initTheme(window",
		"app.js must initialise the theme (re-apply + track OS changes on 'system')")
	assertContains(t, "app.js", "setThemePreference(window",
		"app.js must persist the chosen theme when a radio changes")
	assertContains(t, "style.css", `:root[data-theme="light"]`,
		"style.css must define the light palette keyed on the data-theme attribute")
}

// TestTokensUIWiring guards the /tokens page wiring. The create/list/revoke +
// reveal-once logic lives in app.js's IIFE (not jsdom-importable), so pin it by
// string search; the pure list rendering is unit-tested in
// internal/ui/jstests/tokens.test.js.
func TestTokensUIWiring(t *testing.T) {
	assertContains(t, "index.html", `id="tokens-view"`,
		"index.html must keep the #tokens-view page section")
	assertContains(t, "index.html", `id="new-token-modal"`,
		"index.html must keep the new-token modal")
	assertContains(t, "index.html", `id="token-reveal"`,
		"index.html must keep the reveal-once panel (a token is shown only once)")
	assertContains(t, "index.html", `id="token-reveal-value"`,
		"index.html must keep the element that shows the raw token once")
	assertContains(t, "index.html", `id="profile-tokens-link"`,
		"the profile modal must keep the 'Manage API tokens' link (the page's entry point)")
	assertContains(t, "app.js", `'/api/tokens'`,
		"app.js must call the /api/tokens endpoint to list/create tokens")
	assertContains(t, "app.js", "renderTokenList",
		"app.js must render the token list via views/tokens.js")
	assertContains(t, "app.js", "Array.isArray(body.items)",
		"app.js loadTokens must read the {items,...} list envelope (body.items), not the raw response body")
	assertContains(t, "app.js", "body.token",
		"app.js must read body.token from the create response to reveal it once")
	assertContains(t, "app.js", "Revoke token",
		"app.js must confirm before revoking a token (destructive)")
	assertContains(t, "app.js", `router.register('/tokens'`,
		"app.js must register the /tokens SPA route")
	assertContains(t, "index.html", `id="new-token-expiry"`,
		"the new-token form must offer an expiry choice")
	assertContains(t, "app.js", "getElementById('new-token-expiry')",
		"submitNewToken must read the expiry select")
	assertContains(t, "app.js", "payload.expires_in_days = expiryDays",
		"submitNewToken must send expires_in_days to POST /api/tokens when an expiry is chosen")
}

// TestTablistKeyboardNavWired guards the WAI-ARIA tablist keyboard pattern on
// the app-detail settings tabs. The keydown wiring lives inside app-detail.js's
// mount closure (not jsdom-importable), so pin it by string search: the module
// must import createTablistNav, attach it to the tab strip, and maintain a
// roving tabindex per render. The pure resolver + DOM behaviour are unit-tested
// in internal/ui/jstests/tablist-keys.test.js.
func TestTablistKeyboardNavWired(t *testing.T) {
	assertContains(t, "views/app-detail.js", "createTablistNav",
		"app-detail.js must wire createTablistNav onto the settings tab strip for arrow-key navigation")
	assertContains(t, "views/app-detail.js", "'tabindex'",
		"app-detail.js must set a roving tabindex per render so only the active tab is in the page Tab order")
	assertContains(t, "index.html", `role="tablist"`,
		"the settings tab strip must keep role=tablist for the keyboard pattern to apply")
}

// TestAppDetailTabSwitchIsInPlace guards the three-part wiring that makes a tab
// click a change within the page rather than a page load: the router's same-key
// update path, app.js registering all three app-detail patterns under one key,
// and app-detail.js returning an update() for the router to call. Break any one
// link and every tab click silently returns to hiding the view, blanking the
// metric tiles, resetting the scroll position and stealing focus - which is what
// made it feel like a full refresh. The router behaviour itself is unit-tested
// in internal/ui/jstests/router.test.js; the app-detail closure is not
// jsdom-importable, so it is pinned by string search.
func TestAppDetailTabSwitchIsInPlace(t *testing.T) {
	assertContains(t, "router.js", "typeof current.update === 'function'",
		"the router must hand a navigation between same-key routes to the mounted view's update() instead of unmount+mount")
	assertContains(t, "app.js", "const appDetailKey = (p) => 'app-detail:' + p.slug",
		"app.js must give the app-detail routes a shared per-app key so a tab switch resolves to the mounted view")
	assertContains(t, "app.js", "key: appDetailKey",
		"every app-detail route pattern must register with the shared key, or the tab it names remounts the page")
	assertContains(t, "views/app-detail.js", "      update,",
		"app-detail.js must return an update() from its mount, or the router falls back to remounting on every tab click")
	assertContains(t, "views/app-detail.js", "function seedStats(app)",
		"stat seeding must stay a mount-only step; re-running it on a tab switch blanks the live metric tiles to an em dash")
}

// TestAppDetailCacheInvalidationWired guards the other half of the in-place tab
// switch: rendering a tab from a cached envelope is only correct while nothing
// has changed. api() in app.js announces every successful mutating request and
// app-detail.js listens, so a tab opened after a rollback, a restart or a config
// save refetches before it renders instead of drawing the state from before the
// action. Break the announcement and the page silently shows stale data - the
// worst failure mode this change could have. The counting rule behind it is
// unit-tested in internal/ui/jstests/freshness.test.js.
func TestAppDetailCacheInvalidationWired(t *testing.T) {
	assertContains(t, "app.js", "new CustomEvent('shinyhub:mutated'",
		"api() must announce every successful mutating request so cached views know they are out of date")
	assertContains(t, "views/app-detail.js", "document.addEventListener('shinyhub:mutated'",
		"app-detail.js must listen for the mutation announcement, or a tab opened after an action renders pre-action data")
	assertContains(t, "views/app-detail.js", "if (freshness.isStale()) await refetch(",
		"a stale envelope must be refetched BEFORE the panel renders, not repainted behind it")
}

// TestRouterErrorBoundaryWired guards the global error boundary. A throw inside
// a view mount function once blanked the whole dashboard (v0.8.7). The router
// now catches mount throws and calls an onError callback; app.js must pass one
// that reveals the generic #route-error-view instead of leaving a blank shell,
// and must register window error/unhandledrejection nets. If any link breaks,
// the blank-dashboard failure class can silently return.
func TestRouterErrorBoundaryWired(t *testing.T) {
	assertContains(t, "router.js", "opts.onError",
		"router mount must be guarded and route failures to opts.onError; see internal/ui/jstests/router.test.js")
	assertContains(t, "app.js", "createRouter({ onError:",
		"app.js must pass an onError handler to createRouter so a failed mount reveals the error view")
	assertContains(t, "app.js", "onMounted: clearRouteError",
		"app.js must pass onMounted so a successful navigation clears a prior route-error view (else it lingers beside the healthy page)")
	assertContains(t, "index.html", `id="route-error-view"`,
		"index.html must keep #route-error-view as the visible fallback when a view mount fails")
	assertContains(t, "app.js", "unhandledrejection",
		"app.js must register a window unhandledrejection net so async failures are observable")
}

// TestAppsGridErrorWiring guards that app.js passes a showError callback into
// the apps-grid ctx so a failed initial /api/apps load surfaces the shared
// error banner instead of a silent empty grid. See views/apps-grid.js and its
// jstest for the view-side behaviour.
func TestAppsGridErrorWiring(t *testing.T) {
	assertContains(t, "app.js", "showError:",
		"app.js must pass a showError callback into the apps-grid ctx so a failed load is visible")
	assertContains(t, "views/apps-grid.js", "ctx.showError",
		"apps-grid must call ctx.showError on load failure rather than returning a silent empty grid")
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

// TestResourceLimitsUIWiring guards the Configuration → Resources controls
// against the API contract: per-app memory/CPU limits are enforced in BOTH
// native and docker mode, the CPU ceiling is 6400 (64 cores, matching the
// widened API), and the envelope's resource_enforcement {memory,cpu} drives a
// "not enforced" warning so an operator on a host without cgroup delegation is
// not misled. The save path must confirm the restart a limit change triggers.
func TestResourceLimitsUIWiring(t *testing.T) {
	assertContains(t, "index.html", `id="resources-cpu"`,
		"the Resources section must keep the CPU quota input")
	assertContains(t, "index.html", `max="6400"`,
		"the CPU quota input must allow up to 6400 (64 cores), matching the widened API")
	assertContains(t, "app.js", "app.resource_enforcement",
		"the Resources render must read the envelope's resource_enforcement to warn when native enforcement is absent; see internal/api/apps.go (resource_enforcement)")
	assertContains(t, "app.js", "6400",
		"saveResources must validate the CPU quota against the 6400 ceiling")
	assertContains(t, "app.js", "will restart the app and drop all active sessions",
		"saveResources must confirm the restart a resource-limit change triggers")
}

// TestEnvListUnwrapsResponse guards the env-list consumer.
// GET /api/apps/:slug/env returns the standard {items,...} list envelope
// (internal/api/env.go handleListAppEnv via writeList) and refreshEnvList in
// app.js reads data.items.
func TestEnvListUnwrapsResponse(t *testing.T) {
	assertContains(t, "app.js", "data.items",
		"GET /api/apps/:slug/env returns {items: [...]}; see internal/api/env.go handleListAppEnv")
}

// TestDataTabUnwrapsResponse guards the data-tab consumer.
// GET /api/apps/:slug/data returns the standard {items,...} list envelope with
// quota_mb/used_bytes as sibling keys (internal/api/data.go handleDataList via
// writeList) and refreshDataTab in app.js reads env.items.
func TestDataTabUnwrapsResponse(t *testing.T) {
	assertContains(t, "app.js", "env.items",
		"GET /api/apps/:slug/data returns {items, quota_mb, used_bytes}; see internal/api/data.go handleDataList")
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

// TestAppCardHasExplicitManageLink pins the card's two destinations. The app
// name leads to administration and says so to assistive technology; Open app
// launches the active release in a new tab. The rest of the card is not a
// nested/oversized link, leaving text selectable and actions unambiguous.
func TestAppCardHasExplicitManageLink(t *testing.T) {
	assertContains(t, "app.js", "? `Manage ${app.name}`",
		"a manageable app-name link must identify its management destination")
	assertContains(t, "app.js", ": `View ${app.name}`",
		"a read-only app-name link must not promise management access")
	assertContains(t, "app.js", "openLink.setAttribute('aria-label', `Open ${app.name} app in a new tab`)",
		"the launch action must identify its new-tab destination")
	assertNotContains(t, "app.js", "app-card-body-link",
		"the whole card must not be a link when it contains independent actions")
	assertContains(t, "style.css", ".app-card-title:focus-visible",
		"the explicit app-name link needs a visible keyboard focus state")
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

// TestAuditTabUsesCapability pins the audit-surface gating on the
// server-computed can_read_audit capability (admin, or operator behind
// auth.operator_audit_access) instead of a client-side role comparison, so
// enabling the flag lights the tab up without a UI change.
func TestAuditTabUsesCapability(t *testing.T) {
	assertContains(t, "app.js", "state.canReadAudit = !!payload.can_read_audit",
		"showLoggedIn must capture the server-computed audit capability")
	assertContains(t, "app.js", "tabAudit.hidden = !state.canReadAudit",
		"the Audit nav tab must be gated on the capability, not role==='admin'")
	assertContains(t, "views/overview.js", "ctx.state.canReadAudit",
		"the Overview activity feed must be gated on the capability")
	assertNotContains(t, "views/overview.js", "role === 'admin'",
		"overview.js must not re-derive audit access from the role")
}

// TestAccessVisibilityLabelsTeachSemantics pins the corrected visibility copy.
// "shared" admits every signed-in user (the membership check is bypassed) and
// "private" is where member/group grants apply; the old copy said "Private is
// owner-only; Shared adds members and groups below", which inverted the model
// and pushed operators toward over-exposing apps.
func TestAccessVisibilityLabelsTeachSemantics(t *testing.T) {
	assertContains(t, "index.html", "Private (members only)",
		"the private radio label must say membership is what admits people")
	assertContains(t, "index.html", "Shared (all signed-in users)",
		"the shared radio label must say every signed-in user can open the app")
	assertContains(t, "index.html", "Public (no sign-in)",
		"the public radio label must say no sign-in is required")
	assertNotContains(t, "index.html", "Private is owner-only",
		"the visibility description must not claim private excludes members/groups")
	assertNotContains(t, "index.html", "Shared adds members and groups",
		"the visibility description must not conflate shared with membership")
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

// TestDeploymentsLoadReadsListEnvelope pins that the deployments tab reads the
// standard {items,...} list envelope (handleListDeployments emits it via
// writeList). Reading the raw response as an array would silently render an
// empty history once the server wraps the list.
func TestDeploymentsLoadReadsListEnvelope(t *testing.T) {
	assertContains(t, "views/app-detail.js", "body.items",
		"app-detail.js deployments load() must read the {items,...} envelope (body.items), not the raw response body")
}

// TestDeploymentsLoadDoesNotMask404AsEmpty guards the deployments tab error
// surface. The server (internal/api/apps.go handleListDeployments) returns
// `200 {items:[]}` for an existing app with no deployments, and only emits 404 when
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
	assertContains(t, "style.css", "grid-template-columns: minmax(9rem, max-content) minmax(14rem, 1fr) minmax(6rem, auto) auto",
		"the four-column Deployments grid must let both version and source grow without overlapping the timestamp or action")
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
// so operators see fire times in the schedule's own zone. The form must not
// present a browser-local preview as if it were authoritative server behavior.
func TestScheduleTimezoneFields(t *testing.T) {
	assertContains(t, "app.js", "s.effective_timezone",
		"schedule table must read s.effective_timezone from the DTO to render the zone and next_fire correctly")
	assertContains(t, "app.js", "s.timezone_inherited",
		"schedule table must read s.timezone_inherited to show the (inherited) hint when no per-schedule timezone is stored")
	assertContains(t, "app.js", "s.timezone",
		"schedule form must read s.timezone when editing an existing schedule to populate the timezone field")
	assertContains(t, "app.js", "sched-timezone",
		"schedule form must reference sched-timezone so the timezone input is populated and submitted")
	assertContains(t, "app.js", "evaluated in ${zone}",
		"cron form hint must name the selected or server timezone without inventing browser-local fire times")
	assertContains(t, "index.html", "sched-timezone",
		"schedule form modal in index.html must have a sched-timezone input for the optional per-schedule timezone")
}

// TestScheduleDSTAdvisoryWired guards the DST fall-back double-fire surface.
// The server computes the advisory and returns it on the schedule DTO as
// dst_advisory; the schedule table must render it inline in the cron cell via
// the dstAdvisoryMarkup helper. If the import or the call site is dropped the
// double-fire footgun goes silent in the UI again.
func TestScheduleDSTAdvisoryWired(t *testing.T) {
	assertContains(t, "app.js", "dstAdvisoryMarkup,",
		"app.js must import dstAdvisoryMarkup so the schedule table can surface the DST fall-back advisory")
	assertContains(t, "app.js", "dstAdvisoryMarkup(s)",
		"schedule table cron cell must call dstAdvisoryMarkup(s) to render the dst_advisory from the DTO")
	assertContains(t, "views/schedule-ui.js", "schedule.dst_advisory",
		"schedule-ui helper must read dst_advisory from the schedule DTO computed by the server")
}

func TestScheduleRunActivationErrorsAreVisibleAndEscaped(t *testing.T) {
	assertContains(t, "app.js", "activationErrorDetail(run)",
		"schedule run history must read the activation_error field returned by the API")
	assertContains(t, "app.js", "escapeHtml(activationError)",
		"historical activation errors are external process text and must be HTML-escaped")
	assertContains(t, "style.css", ".schedule-run-activation-error",
		"historical activation errors need a resilient visible treatment in run history")
}

// TestScheduleSurfaceSafetyContracts guards the operator-facing semantics that
// are easiest to regress when schedule API fields evolve. Mutation controls
// start hidden until the capability envelope proves they are allowed, form
// failures are announced, and the selected detail is invalidated when its DTO
// changes between polls.
func TestScheduleSurfaceSafetyContracts(t *testing.T) {
	assertContains(t, "index.html", `id="schedules-add-btn" hidden`,
		"the Add schedule control must fail closed until can_manage is known")
	assertContains(t, "index.html", "Job success alone does not prove",
		"the schedules introduction must not imply that job success proves serving-data activation")
	assertContains(t, "index.html", `id="schedule-form-error" class="error" role="alert"`,
		"schedule form failures must be announced to assistive technology")
	assertContains(t, "views/schedule-ui.js", "No future app action",
		"a disabled activation policy must be described as controlling future runs")
	assertContains(t, "app.js", "Earlier policy ·",
		"retained activation attribution must be labelled as originating under an earlier policy")
	assertContains(t, "app.js", "surface.renderedDetailSignature === detailSignature",
		"selected schedule detail must be invalidated when its DTO changes during polling")
	assertContains(t, "views/schedule-ui.js", "last_success_age_s advances on every poll",
		"volatile relative age must not invalidate selected detail or discard run-history pagination")
	assertContains(t, "views/schedule-ui.js", "case 'skipped_overlap':",
		"schedule status labels must recognize the backend skipped-overlap outcome")
	assertContains(t, "views/schedule-ui.js", "case 'register': return 'On registration';",
		"run history must label the backend registration trigger honestly")
	assertContains(t, "views/schedule-ui.js", "case 'missed': return 'Missed run';",
		"run history must label the backend missed-run trigger honestly")
	assertContains(t, "views/schedule-ui.js", "case 'schedule':",
		"run history must label the backend cron-boundary trigger honestly")
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
	// interrupted run, so the UI must render a neutral placeholder rather than
	// coercing null to exit 0 or the string "null".
	assertContains(t, "app.js", "run.exit_code == null ? '—'",
		"run-history must render a neutral placeholder for a null exit_code")
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

// TestWorkerIsolationControlsWired guards the Configuration -> Scaling worker
// isolation controls. app.js must read app.worker_isolation and
// app.worker_max_workers and app.worker_warm_spares from the GET envelope,
// include worker_isolation in the
// scaling PATCH payload, and call workerCapacityLine so the host-capacity
// helper line stays live. If any of these wires drift the controls silently
// stop reflecting or persisting the isolation mode.
func TestWorkerIsolationControlsWired(t *testing.T) {
	// The import must be present so the helper is callable.
	assertContains(t, "app.js", "/static/views/worker-isolation.js",
		"app.js must import the worker-isolation helper module")
	assertContains(t, "app.js", "workerCapacityLine",
		"app.js must call workerCapacityLine to recompute the host-capacity helper line on input")

	// Populate path: read the API envelope fields.
	assertContains(t, "app.js", "worker_isolation",
		"app.js scaling populate must read app.worker_isolation from the GET envelope")
	assertContains(t, "app.js", "worker_max_workers",
		"app.js scaling populate must read app.worker_max_workers from the GET envelope")
	assertContains(t, "app.js", "worker_warm_spares",
		"app.js scaling populate must read app.worker_warm_spares from the GET envelope")

	// Save path: include the isolation mode in the PATCH payload.
	assertContains(t, "app.js", "worker_isolation: workerIsolation",
		"saveScalingSettings must include worker_isolation in the PATCH payload")
	assertContains(t, "app.js", "worker_warm_spares: workerWarmSpares",
		"saveScalingSettings must include worker_warm_spares in the PATCH payload")

	// HTML: the isolation select and capacity helper must exist.
	assertContains(t, "index.html", `id="worker-isolation"`,
		"index.html must expose #worker-isolation as the isolation mode select")
	assertContains(t, "index.html", `id="worker-max-workers"`,
		"index.html must expose #worker-max-workers as the max workers input")
	assertContains(t, "index.html", `id="worker-warm-spares"`,
		"index.html must expose #worker-warm-spares as the warm worker input")
	assertContains(t, "index.html", `id="worker-capacity"`,
		"index.html must expose #worker-capacity as the slot workerCapacityLine populates")

	// The pure helper module must export the function the tests and app.js use.
	assertContains(t, "views/worker-isolation.js", "export function workerCapacityLine",
		"worker-isolation.js must export workerCapacityLine so it can be imported by app.js and unit-tested")
}

// TestKeepWarmInertNoteWired guards the Keep warm advisory for elastic
// isolation. A positive min_warm_replicas under grouped / per_session is
// stored but inert (an elastic pool runs no standing replicas and reports idle
// as healthy), and the field is the one place an operator would otherwise
// keep raising the floor waiting for replicas that never come. The note is
// computed by keepWarmInertNote from the EFFECTIVE isolation (the raw column
// is empty when the app inherits an elastic fleet default) and rendered into
// #min-warm-inert-warning; each wire fails silently on its own.
func TestKeepWarmInertNoteWired(t *testing.T) {
	assertContains(t, "views/worker-isolation.js", "export function keepWarmInertNote",
		"worker-isolation.js must export keepWarmInertNote so it can be imported by app.js and unit-tested")
	assertContains(t, "app.js", "keepWarmInertNote",
		"app.js must call keepWarmInertNote to compute the Keep warm advisory")
	assertContains(t, "app.js", "app.effective_worker_isolation || app.worker_isolation",
		"app.js must evaluate the Keep warm advisory against the effective isolation on load")
	assertContains(t, "index.html", `id="min-warm-inert-warning"`,
		"index.html must expose #min-warm-inert-warning as the slot keepWarmInertNote populates")
}

// TestRenderPacingControlWired guards the Configuration -> Render pacing
// control, the dashboard surface for apps.render_seconds. Three wires carry it,
// and each fails silently on its own:
//
//   - GET /api/apps/:slug must emit the envelope-level render_pacing block, and
//     app.js must read it off the ENVELOPE. normalizeAppEnvelope folds only
//     app-level fields, so reading app.render_pacing would be undefined forever
//     and the advice line would silently degrade to the no-block wording.
//   - the PATCH response's block must replace the stale one, or the advice keeps
//     describing the value the operator just replaced.
//   - the section must be registered with the dirty tracker, or Save stays
//     disabled (it is hidden+disabled in the markup) and the control is inert.
func TestRenderPacingControlWired(t *testing.T) {
	assertContains(t, "app.js", "/static/views/render-pacing.js",
		"app.js must import the render-pacing helper module")
	assertContains(t, "app.js", "parseRenderSeconds",
		"saveRenderPacing must validate through parseRenderSeconds so a blank field is rejected rather than persisted as 0 (pacing off)")
	assertContains(t, "app.js", "renderPacingAdvice",
		"app.js must render the advisory line through renderPacingAdvice so the mid-edit wording stays covered by unit tests")

	// Populate path: the advisory is an ENVELOPE field, not an app column.
	assertContains(t, "app.js", "envelope.render_pacing",
		"populateGeneralTab must read render_pacing off the GET envelope (handleGetApp emits it alongside app, not on it)")
	assertContains(t, "app.js", "app.render_seconds",
		"populateGeneralTab must seed #render-seconds from app.render_seconds")
	assertContains(t, "views/app-detail.js", "renderConfiguration(panels.configuration, app, ctx, body)",
		"app-detail.js must pass the raw GET body into renderConfiguration, or the envelope-level render_pacing block never reaches the control")

	// Save path: PATCH the single field and adopt the response's fresh advisory.
	assertContains(t, "app.js", "render_seconds: parsed.value",
		"saveRenderPacing must PATCH render_seconds with the parsed value")
	assertContains(t, "app.js", "body.render_pacing",
		"saveRenderPacing must adopt the PATCH response's render_pacing block, or the advice keeps describing the previous setting")
	assertContains(t, "app.js", `registerSettingsSection('render'`,
		"the render section must be registered with the dirty tracker, or its Save button never enables")

	// HTML: the input, the advice slot, and the explicit-save controls.
	assertContains(t, "index.html", `id="render-seconds"`,
		"index.html must expose #render-seconds as the render cost input")
	assertContains(t, "index.html", `id="render-pacing-advice"`,
		"index.html must expose #render-pacing-advice as the slot renderPacingAdvice populates")
	assertContains(t, "index.html", `id="render-save-btn"`,
		"index.html must expose #render-save-btn for the explicit-save model every other settings section uses")
	assertContains(t, "index.html", `id="render-dirty"`,
		"index.html must expose #render-dirty so the unsaved-changes hint matches the other settings sections")
	// The advice changes without a user gesture on the element (it updates as the
	// operator types elsewhere in the section), so it must announce itself.
	assertContains(t, "index.html", `id="render-pacing-advice" class="scaling-ceiling" aria-live="polite"`,
		"the advice line must be an aria-live region, like the admission-ceiling helper it sits beside")

	// The pure helper module must export what app.js and the unit tests import.
	assertContains(t, "views/render-pacing.js", "export function parseRenderSeconds",
		"render-pacing.js must export parseRenderSeconds so it can be imported by app.js and unit-tested")
	assertContains(t, "views/render-pacing.js", "export function renderPacingAdvice",
		"render-pacing.js must export renderPacingAdvice so it can be imported by app.js and unit-tested")
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
		"/internal/runtime-bundle/",
		"entrypoint.sh must fetch the bundle from GET /internal/runtime-bundle/{digest}; changing this path requires updating the entrypoint too")
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

// TestLifecycleControlsWiring pins the Sleep / Stop / Start wiring that jsdom
// cannot reach: appCardActions is unit-tested, but the code that turns its
// booleans into menu items and HTTP calls lives inside the app.js IIFE, which
// is not importable. Without these pins the decision module and the render can
// drift apart silently - the menu would keep showing an item the module hides,
// or call an endpoint that does not exist.
func TestLifecycleControlsWiring(t *testing.T) {
	assertContains(t, "app.js", "/sleep",
		"the Sleep control must POST /api/apps/{slug}/sleep")
	assertContains(t, "app.js", "syncCardActions(kebab, cardActions)",
		"the card menu's visible rows must come from appCardActions, not from an inline status check that would drift from the tested module")
	for _, field := range []string{"acts.showSleep", "acts.showStop", "acts.showStart"} {
		assertContains(t, "app.js", field,
			"syncCardActions must decide each lifecycle row from the appCardActions result")
	}
	assertContains(t, "app.js", "if_not_running=true",
		"Start must use the idempotent restart form so a second click does not cycle an app another operator already brought up")
	assertContains(t, "app.js", "'stop', 'sleep',",
		"the audit-log filter must list the sleep action or sleep events render as an unknown action and cannot be filtered")

	// The detail header shows the same actions as the card, driven by the same
	// appCardActions result so the two surfaces cannot disagree about what is
	// offered for a given app.
	for _, id := range []string{"app-detail-sleep", "app-detail-stop", "app-detail-start"} {
		assertContains(t, "index.html", id,
			"the app detail header kebab must offer the same lifecycle actions as the card")
		assertContains(t, "app.js", id,
			"each detail-header lifecycle item must be wired and shown/hidden from appCardActions")
	}

	// The Sleep decision must read the RESOLVED isolation the API computes
	// (db.App.EffectiveWorkerIsolation), not the raw column, which is empty for
	// an app inheriting runtime.default_worker_isolation. Reading it raw on an
	// elastic-by-default server offers Sleep on every inheriting app and every
	// click 409s.
	assertContains(t, "views/app-card-actions.js", "app.effective_worker_isolation",
		"appCardActions must decide elastic-ness from the server-resolved effective_worker_isolation, not the raw per-app column")

	// deploy_count is denormalized and its post-deploy increment is
	// log-and-continue, so it can read 0 for an app that is deployed and running.
	// Lifecycle eligibility has to key off the durable deployments row instead, or
	// a lost increment hides Start on a stopped app with no way back up.
	assertContains(t, "views/app-card-actions.js", "app.last_deployment_status === 'succeeded'",
		"appCardActions must gate lifecycle actions on the durable deployment row, not on the denormalized deploy_count alone")

	// Isolation is saved from the Configuration tab while the detail header is on
	// screen, and the metrics poller only merges status and deploying. The save
	// path therefore has to fold the reloaded app back into detailApp, or the
	// header keeps offering Sleep for an app that just became elastic.
	assertContains(t, "app.js", "Object.assign(detailApp, fresh)",
		"saveScalingSettings must merge the reloaded app into detailApp so the header menu follows a saved isolation change")
}

// TestLifecycleMenusFollowPolledStatus guards the staleness bug the live check
// caught: the 10s metrics poll rewrites the stored app model and repaints the
// status badge and detail pill in place, without re-rendering the grid. Before
// this, an app that hibernated on the idle watchdog (or woke on a visitor's
// request) showed its new badge while its menu still offered the actions of the
// state it had left - a "Running" card whose only menu item was Start.
//
// Both re-syncs must sit inside the onMetrics handler, so the assertions pin
// the call sites rather than merely the existence of the two functions.
func TestLifecycleMenusFollowPolledStatus(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	src := string(b)
	start := strings.Index(src, "onMetrics:")
	if start < 0 {
		t.Fatal("app.js: could not find the metrics poller's onMetrics handler")
	}
	handler := src[start:]
	if i := strings.Index(handler, "\n    },\n"); i > 0 {
		handler = handler[:i]
	}
	for _, call := range []string{
		"syncCardActions(kebabEl, appCardActions(gridApp, canManageApp(state.user, gridApp)))",
		"syncDetailHeaderActions(detailApp)",
	} {
		if !strings.Contains(handler, call) {
			t.Errorf("expected the metrics poller's onMetrics handler to call %q so a status\n"+
				"change it observes updates the lifecycle menu, not just the badge", call)
		}
	}

	// Order matters as much as presence. The badge merge is what writes the
	// polled status and the transient deploying flag onto the stored app model,
	// and appCardActions reads both. Re-syncing the menu before the merge would
	// derive it from the previous tick's model, so a deploy that just started
	// would keep offering Sleep and Stop for another 10 seconds.
	for _, pair := range []struct{ merge, resync string }{
		{"updateCardStatusBadge(badgeEl, gridApp, liveStatus, formatStatus)", "syncCardActions(kebabEl,"},
		{"updateStatusPill(pillEl, detailApp, liveStatus, formatStatus)", "syncDetailHeaderActions(detailApp)"},
	} {
		mergeAt := strings.Index(handler, pair.merge)
		resyncAt := strings.Index(handler, pair.resync)
		if mergeAt < 0 {
			t.Errorf("expected the metrics poller's onMetrics handler to call %q", pair.merge)
			continue
		}
		if resyncAt < 0 {
			t.Errorf("expected the metrics poller's onMetrics handler to call %q", pair.resync)
			continue
		}
		if mergeAt > resyncAt {
			t.Errorf("expected %q to run before %q: the merge writes the polled status and the\n"+
				"deploying flag that the menu is derived from, so re-syncing first leaves the menu\n"+
				"one tick behind the badge", pair.merge, pair.resync)
		}
	}

	// A lifecycle action taken from the detail header must repaint the pill from
	// the same merge, not wait for the next tick: otherwise the header reads
	// "Sleeping" directly above a Stop item until the poller catches up.
	assertContains(t, "app.js", "updateStatusPill(pillEl, detailApp, live, formatStatus)",
		"lifecycleAction must repaint the detail status pill from the response status, on the same merge the poller uses")

	// The rows have to exist in the DOM to be toggled: a menu built from only
	// the currently-applicable actions cannot be re-synced in place.
	assertContains(t, "app.js", "kebab.dataset.slug = app.slug",
		"the card kebab needs its slug so the poller can find the right card's menu")
	assertNotContains(t, "app.js", "].filter(([, , show]) => show)",
		"the card menu must render all lifecycle rows and hide them per state; filtering at build time leaves nothing for the poller to re-sync")
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
	// The helper module is the single source of fleet truth and reads both the
	// ownership field and the server-derived convergence envelope. If either
	// contract changes, fleet-ui.js breaks here.
	assertContains(t, "views/fleet-ui.js", "app.managed_by",
		"fleet ownership derives from the managed_by API field")
	assertContains(t, "views/app-detail.js", "body.fleet_state",
		"app detail passes the fleet_state API envelope to the fleet presentation helpers")

	// Apps grid wiring: imports the helper, preserves fleet context as a quiet
	// card fact, and segments the list.
	assertContains(t, "app.js", "/static/views/fleet-ui.js",
		"apps grid imports the fleet-ui helper module")
	assertContains(t, "views/app-card-facts.js", "Fleet managed",
		"apps grid cards preserve fleet ownership without adding another header badge")
	assertContains(t, "views/app-card-facts.js", "Managed by ${app.managed_by}",
		"the fleet fact tooltip must retain the specific fleet id")
	assertContains(t, "app.js", "segmentApps",
		"apps grid filters by the All/Fleet-managed/Unmanaged segment")

	// Card-header overflow guard. .app-card is overflow:visible (so the kebab
	// dropdown can escape) and .app-header is a nowrap row, so a badge appended
	// straight into it renders out past the card border. Two pieces have to
	// stay together: the wrapping container app.js emits and its stylesheet
	// rule.
	assertContains(t, "app.js", "app-header-badges",
		"card badges go in a wrapping container, not straight into the nowrap .app-header")
	assertContains(t, "style.css", ".app-header-badges",
		"style.css must style the badge container app.js emits, or badges overflow the card")
	// The card label is a constant. That is what makes its width independent of
	// an operator-chosen fleet id, so nothing here can overflow no matter how
	// long the id is; the id lives in the tooltip instead.
	assertContains(t, "views/fleet-ui.js", "FLEET_BADGE_COMPACT_LABEL = 'fleet'",
		"the card fleet label is a fixed word, not the fleet id, so card width cannot depend on an operator-chosen id")
	assertContains(t, "style.css", ".app-header-badges .badge-fleet",
		"the card fleet chip is dressed down to metadata weight rather than competing with the status badge")
	assertContains(t, "index.html", "apps-segment",
		"apps toolbar exposes the All/Fleet-managed/Unmanaged control")

	// App-detail wiring: neutral ownership, proven convergence state, and the
	// aligned Overview review card. The old digest disclaimer/CLI coaching slot
	// is intentionally gone.
	assertContains(t, "views/app-detail.js", "/static/views/fleet-ui.js",
		"app detail imports the fleet-ui helper module")
	assertContains(t, "views/app-detail.js", "renderFleetBadges",
		"app detail renders fleet ownership and meaningful convergence badges")
	assertContains(t, "views/app-detail.js", "makeFleetStateCard",
		"the Overview renders the always-reviewable temporary changes or incomplete convergence card")
	assertContains(t, "views/app-detail.js", "annotateFleetChanges",
		"configuration fields show which saved values are temporary and what the fleet declares")
	assertContains(t, "app.js", "await refreshDetailFleetState(slug)",
		"successful settings saves immediately re-fetch proven fleet state instead of leaving mount-time indicators stale")
	assertContains(t, "views/app-detail.js", "refreshFleetSurfaces",
		"the fleet refresh repaints badges, the Overview card, and field annotations without remounting the form")
	assertContains(t, "style.css", ".overview-grid.has-fleet-state",
		"fleet state participates in an explicit grid row instead of being randomly inserted")
	assertNotContains(t, "views/fleet-ui.js", "shinyhub fleet",
		"dashboard fleet state must not coach operators to run CLI commands")
	assertNotContains(t, "views/fleet-ui.js", "Hide changes",
		"temporary changes stay reviewable without a redundant disclosure toggle")
	assertNotContains(t, "index.html", `id="app-detail-fleet"`,
		"the obsolete top digest/disclaimer bar must not return")
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

// TestMetricsAvailableWiring pins the PID-less metrics contract to the detail
// surface. Routine CPU/RAM was deliberately removed from the scan-first app
// cards, while replica details must still distinguish unavailable from zero.
func TestMetricsAvailableWiring(t *testing.T) {
	assertContains(t, "views/replica-display.js", "metrics_available",
		"the replica detail must honor metrics_available for PID-less backends")
	assertNotContains(t, "app.js", "m.metrics_available",
		"the app index must not bring routine CPU/RAM telemetry back onto cards")
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

// TestGridAutoscaleFact pins useful autoscale context without a second header
// badge. A hibernated app with min=0 explains that state as policy, while all
// other autoscale detail stays on the app overview.
func TestGridAutoscaleFact(t *testing.T) {
	assertContains(t, "views/app-card-facts.js", "app.autoscale_enabled",
		"card facts must read autoscale state when it explains a sleeping app")
	assertContains(t, "views/app-card-facts.js", "Scales to zero",
		"a hibernated scale-to-zero app must describe its policy")
	assertNotContains(t, "app.js", "badge-autoscale",
		"autoscale must not add another variably-sized badge to every card header")
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

	// SSE log streams surface a disconnect/reconnect state instead of freezing
	// silently. The app-detail viewer intentionally lets EventSource reconnect;
	// it reports that state in its dedicated status region.
	assertContains(t, "app.js", "(log stream disconnected)",
		"app.js log-pane SSE onerror must append a disconnect notice")
	assertContains(t, "views/logs-ui.js", "Reconnecting ·",
		"the app-detail log viewer must expose its reconnect state")
	assertContains(t, "views/logs-ui.js", "stream.onerror",
		"the app-detail log viewer must handle EventSource disconnects")

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
	assertContains(t, "views/fleet-health.js", "export function activationAttentionTooltip",
		"fleet-health.js must expose attributable activation-attention detail")
	assertContains(t, "app.js", "activationAttentionTooltip(s)",
		"renderFleetHealth must include activation-attention detail in the banner tooltip and accessible name")
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
	assertContains(t, "views/app-detail-envelope.js", "body.can_manage",
		"normalizeAppEnvelope must copy body.can_manage onto the app object so canManageApp sees it")
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
	assertContains(t, "app.js", "setOpen(opening, opening ? 'first' : '')",
		"keyboard activation must move focus into an opened role=menu")
	assertContains(t, "app.js", "if (e.key === 'Tab')",
		"Tab must close an open role=menu while allowing focus to continue")
	assertContains(t, "app.js", `role="menuitem" data-kebab`,
		"card action buttons, not their list wrappers, must own menuitem semantics")
	// The header kebab's items are all manager actions, so the whole menu must be
	// hidden for viewers (mirrors the card). That is one of the things
	// syncDetailHeaderActions decides, from the same appCardActions helper the
	// cards use: it reports every action false when canManage is false, and hides
	// the menu when no item applies. app-detail.js must not set the same flag
	// again, because the later writer would silently win.
	assertContains(t, "app.js", "function syncDetailHeaderActions",
		"the app-detail header kebab's per-app visibility must be decided in one place, from appCardActions")
	assertNotContains(t, "views/app-detail.js", "headerKebab",
		"app-detail.js must not also set the header kebab's visibility; ctx.setDetailApp already drives it through syncDetailHeaderActions")
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
// back button. deploymentTimelineModels delegates each attempt to
// deploymentListModels before grouping development sessions; this pins wiring.
func TestDeploymentsMarkCurrent(t *testing.T) {
	assertContains(t, "views/app-detail.js", "deploymentTimelineModels(rows)",
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

// TestResourcesEnforcementHonesty guards that per-app resource limits are
// editable in BOTH native and docker mode, and that the client surfaces the
// envelope's runtime_mode + resource_enforcement so a native host without cgroup
// delegation shows a "not enforced" note rather than silently accepting an
// unenforced limit. The detail view must merge both envelope fields onto app.
func TestResourcesEnforcementHonesty(t *testing.T) {
	assertContains(t, "views/app-detail-envelope.js", "body.runtime_mode",
		"normalizeAppEnvelope must read runtime_mode from the GET envelope")
	assertContains(t, "views/app-detail-envelope.js", "body.resource_enforcement",
		"normalizeAppEnvelope must merge resource_enforcement from the envelope so app.resource_enforcement is defined; see internal/api/apps.go")
	assertContains(t, "app.js", "app.runtime_mode !== 'docker'",
		"the Resources render must treat non-docker as native (limits still apply) and key the enforcement warning off it")
	assertContains(t, "index.html", `id="resources-runtime-note"`,
		"a note element must remain so the render can show the native not-enforced warning")
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
	assertContains(t, "views/logs-ui.js", `id="logs-copy" type="button" class="btn-row"`,
		"the Logs 'Copy visible' button must be a styled .btn-row")
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
	if strings.Contains(nav, `id="tab-launchpad"`) {
		t.Fatal("#primary-nav must expose one Apps-family destination, not a separate Launchpad item")
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
	assertContains(t, "app.js", "el.hidden = true", "view-only sessions must omit the redundant Applications landmark")
	assertContains(t, "app.js", "el.hidden = false", "management sessions must restore the Applications quick-switch landmark")
	assertContains(t, "app.js", "appsSurfaceForSession", "sidebar loading and rendering must share the server-backed session capability policy")
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

// TestBrandingUpdatesAllNodes pins logo replacement hitting every .brand node,
// not just the first. The slot set has grown from sidebar + mobile top bar to
// include the boot splash and the login card, and the walk itself moved out of
// the app.js IIFE into the unit-tested views/branding.js.
func TestBrandingUpdatesAllNodes(t *testing.T) {
	assertContains(t, "views/branding.js", "querySelectorAll('.brand')", "branding must replace every .brand node, not just the first")
	b, err := fs.ReadFile(ui.Static(), "views/branding.js")
	if err != nil {
		t.Fatalf("read views/branding.js: %v", err)
	}
	if strings.Contains(string(b), "querySelector('nav .brand')") {
		t.Fatal("views/branding.js: branding selector must not require 'nav .brand' after the sidebar move")
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
	assertContains(t, "style.css", ":root { --sidebar-w: 232px; --sidebar-w-collapsed: 48px; }",
		"desktop sidebar must use the compact expanded panel and tool rail widths")
	assertContains(t, "style.css", "body.sidebar-collapsed .nav-item,\nbody.sidebar-collapsed .sidebar-about,\nbody.sidebar-collapsed .sidebar-collapse { justify-content: center; padding-left: 0; padding-right: 0; }",
		"collapsed sidebar controls must center their icons in the compact rail")
	assertContains(t, "style.css", "body.sidebar-collapsed .sidebar-footer { margin-top: auto; }",
		"collapsed sidebar footer controls must stay pinned to the bottom")
	assertContains(t, "style.css", "body.sidebar-collapsed [data-tooltip]::before {",
		"collapsed tooltips need a hover bridge between the rail control and popup")
	assertContains(t, "style.css", "border-radius: var(--radius);",
		"collapsed tooltips must use the shared radius token")
	assertContains(t, "index.html", `aria-label="Apps" data-tooltip="Apps"`,
		"the collapsed Apps link must retain an accessible name and a visible tooltip label")
	assertContains(t, "app.js", "const label = on ? 'Expand sidebar' : 'Collapse sidebar'",
		"the collapse control must name its current action")
	assertContains(t, "style.css", "body.sidebar-collapsed .sidebar-apps:not([hidden]) { display: revert; }",
		"mobile restoration must preserve the viewer's intentionally hidden Applications landmark")
	assertContains(t, "style.css", "#sidebar-toggle { min-width: 44px; }",
		"the mobile navigation trigger must meet the 44px touch-target width")
	assertContains(t, "style.css", "body.sidebar-collapsed .nav-item { justify-content: flex-start; padding-left: 0.5rem; padding-right: 0.5rem; }",
		"mobile drawer navigation must restore expanded alignment and padding")
	assertContains(t, "style.css", "#sidebar-toggle,\n  #sidebar .nav-item,\n  #sidebar .sidebar-app,\n  #sidebar .identity-card,\n  #sidebar .sidebar-about { min-height: 44px; }",
		"mobile drawer controls must retain accessible touch targets")
	assertContains(t, "style.css", ".sidebar-brand { display: flex; flex: none; align-items: center; height: 3rem;",
		"sidebar brand slot must keep the primary navigation at a stable vertical position")
	assertContains(t, "style.css", "  min-height: 2.5rem;",
		"identity control must keep the same compact height when its metadata is hidden")
	assertContains(t, "style.css", "  height: 2.5rem;",
		"identity control must not grow when its metadata is visible")
	assertContains(t, "style.css", "  min-height: 2rem;",
		"navigation rows must keep the same height when their labels are hidden")
}

// TestVersionDisplayUsesReleaseNumber pins the human-friendly version display:
// the header/overview show the server's release_number (vN) and date, the
// deployments row renders the release label, and the raw epoch is no longer the
// visible version label (kept only on hover/title).
func TestVersionDisplayUsesReleaseNumber(t *testing.T) {
	assertContains(t, "views/app-detail-envelope.js", "body.release_number",
		"normalizeAppEnvelope must read release_number from the GET envelope")
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
	// CPU and RAM stay on this detail surface rather than competing with release
	// and readiness facts on every index card.
	assertNotContains(t, "app.js", "cardMetricsLabel",
		"the app index must not duplicate routine CPU/RAM from the detail header")
	// CSS: tiles, the running pulse, and a reduced-motion off-switch.
	assertContains(t, "style.css", ".app-detail-stats .stat", "metric tiles must be styled")
	assertContains(t, "style.css", ".app-detail-header .status-pill.is-live::before { animation: none; }",
		"the running pulse must be disabled under prefers-reduced-motion (scoped to the header pill)")
	// The header pill styles must be scoped so they don't clobber the pre-existing
	// schedules .status-pill (status-on/status-off).
	assertContains(t, "style.css", ".app-detail-header .status-pill::before",
		"the header status-pill dot must be scoped to .app-detail-header, not global")
}

// TestAppDetailHeaderDeployWired guards the page-level primary action. The
// empty-state Deploy button already opened the uploader, but the static header
// button used to have no listener and silently did nothing.
func TestAppDetailHeaderDeployWired(t *testing.T) {
	assertContains(t, "app.js", "const dDeploy = document.getElementById('app-detail-deploy')",
		"the app-detail header Deploy button must be wired")
	assertContains(t, "app.js", "openDeployModal(detailApp)",
		"the app-detail header Deploy button must open the uploader for the mounted app")
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
	assertContains(t, "views/app-detail.js", "tabStripScrollTarget({",
		"the active tab must be centered with the testable scroll-target helper")
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
	// The empty-state branch must precede viewer construction so the viewer never
	// opens its EventSources for a never-deployed app.
	b, err := fs.ReadFile(ui.Static(), "views/app-detail.js")
	if err != nil {
		t.Fatalf("read app-detail.js: %v", err)
	}
	js := string(b)
	guard := strings.Index(js, "(app.deploy_count || 0) === 0")
	viewer := strings.Index(js, "return createLogsViewer(")
	if guard < 0 || viewer < 0 || guard > viewer {
		t.Fatal("app-detail.js: the never-deployed guard must come before the multi-replica log viewer is opened")
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
	assertContains(t, "views/fleet-health.js", "stale_schedule_list",
		"fleet-health summary must read stale_schedule_list from GET /api/fleet/health; see internal/api/fleet_health.go")
	assertContains(t, "views/fleet-health.js", "schedule${staleCount === 1 ? '' : 's'} stale",
		"fleet-health headline must include the stale-schedule count part")
	assertContains(t, "app.js", "s.staleSchedules",
		"renderFleetHealth must surface the stale schedule list in the banner tooltip/aria")
	assertContains(t, "views/fleet-health.js", "activation_attention_list",
		"fleet-health summary must read activation_attention_list from GET /api/fleet/health")
	assertContains(t, "views/fleet-health.js", "data activation${activationAttentionCount === 1 ? '' : 's'}",
		"fleet-health headline must include the activation-attention count")
}

// TestAppCardFactsStayOperational pins the deliberately small information
// budget: release, deploy recency, and readiness are scan-worthy; routine
// CPU/RAM belongs on the app detail page. The live poll still refreshes facts.
func TestAppCardFactsStayOperational(t *testing.T) {
	assertContains(t, "app.js", "appCardFacts",
		"the grid card must derive its concise facts from the shared helper")
	assertContains(t, "app.js", ".app-card-facts[data-slug=",
		"the metrics poll must locate and refresh the card facts")
	assertContains(t, "views/app-card-facts.js", "Release #${releaseNumber}",
		"cards must expose the current successful release number")
	assertContains(t, "views/app-card-facts.js", "${ready}/${configured} ready",
		"scaled cards must expose live replica readiness")
	assertNotContains(t, "app.js", "app-metrics",
		"routine CPU/RAM must not clutter the app index")
	assertNotContains(t, "style.css", ".app-metrics",
		"removed index metrics must not retain dead layout styling")
}

// TestAbsentCPURateRendersAsUnknown pins the consumers of a null cpu_percent.
// The server sends null when it has no rate to report (a replica's first poll
// after it starts, or a tier with no local process to sample), and every one of
// these files would otherwise turn that into a confident 0%: a flat idle line at
// exactly the moments an operator is watching, on a restart or a scale-out.
//
// These are the render paths jsdom cannot import. The importable helpers
// (sparklinePoints, headerStats, metricsText, buildOverviewModel) are unit-tested
// against real nulls in internal/ui/jstests/.
func TestAbsentCPURateRendersAsUnknown(t *testing.T) {
	assertContains(t, "views/overview-model.js", "replica.cpu_percent != null",
		"the Overview allocation model must exclude an absent CPU rate instead of treating it as 0%")
	assertContains(t, "views/replica-display.js", "typeof replica.cpu_percent === 'number'",
		"a replica's CPU cell must type-check the rate so null renders as a dash instead of 0.0%")
	assertContains(t, "views/stat-format.js", "cpuAvailable",
		"headerStats must track whether every running replica reported a rate; a partial sum understates the app")
	assertContains(t, "views/sparkline.js", "drawn",
		"the sparkline must skip unmeasured points rather than plotting them at the floor")
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
	assertContains(t, "views/overview.js", "if (model.total === 0)",
		"an empty fleet must take the focused first-run render path instead of showing operational panels")
	assertContains(t, "views/overview.js", "Deploy your first Shiny app",
		"the empty Overview must provide a concrete first-value action")
}

// TestOverviewHostCapacityContract pins the host-capacity block that the
// resource panel falls back to when no app carries an enforced limit. Drop any
// one of these reads and the panel silently regresses to the state this feature
// replaced: "Capacity unavailable" on every row of a perfectly healthy fleet.
func TestOverviewHostCapacityContract(t *testing.T) {
	// GET /api/apps/metrics returns {metrics, generated_at, host?} where host is
	// {cores, cores_source, memory_mb, memory_source} (internal/api/apps.go,
	// type HostCapacity). The key is absent when detection fails, so the reader
	// must tolerate its absence rather than assume the object.
	assertContains(t, "views/overview.js", "b.host",
		"GET /api/apps/metrics returns an optional {host}; the Overview reads body.host")
	assertContains(t, "views/overview-model.js", "host.cores",
		"the CPU denominator comes from host.cores (internal/api HostCapacity.Cores)")
	assertContains(t, "views/overview-model.js", "host.memory_mb",
		"the memory denominator comes from host.memory_mb (internal/api HostCapacity.MemoryMB)")
	assertContains(t, "views/overview-model.js", "host.cores_source",
		"the CPU capacity records where it came from (HostCapacity.CoresSource)")
	assertContains(t, "views/overview-model.js", "host.memory_source",
		"the memory capacity records where it came from (HostCapacity.MemorySource)")
	// A host that reports neither must not become a zero denominator: an absent
	// capacity is not a capacity of nothing.
	assertContains(t, "views/overview-model.js", "cores <= 0) return null",
		"an absent or non-positive core count must yield no capacity, never a zero denominator")
}

// TestLaunchpadContract pins the internal viewer renderer behind the single
// user-facing Apps destination.
func TestLaunchpadContract(t *testing.T) {
	assertContains(t, "app.js", "mountLaunchpad",
		"the role-adaptive Apps route mounts the viewer gallery (views/launchpad.js)")
	assertContains(t, "app.js", "appsSurfaceForSession",
		"the gallery versus management-grid decision must use the shared session policy")
	assertContains(t, "index.html", `id="launchpad-view"`,
		"launchpad.js shows #launchpad-view and renders into #launchpad-body")
	assertContains(t, "index.html", `id="launchpad-body"`,
		"launchpad.js renders the gallery into #launchpad-body")
	assertContains(t, "index.html", `id="tab-apps"`,
		"every role shares one user-facing Apps navigation item")
	assertNotContains(t, "index.html", `id="tab-launchpad"`,
		"Launchpad must not remain as a competing top-level destination")
	assertContains(t, "app.js", "router.register('/launchpad', () => ctx.navigate('/apps', { replace: true }))",
		"legacy Launchpad bookmarks must replace-redirect to canonical Apps")
	assertContains(t, "views/launchpad-model.js", "app.description",
		"GET /api/apps returns description (db.App.Description); the Launchpad tile shows it")
	assertContains(t, "views/launchpad.js", "/app/",
		"a Launchpad tile launches the proxied app at /app/<slug>/")
	// A pure viewer must not reach the operator detail page (logs / deployments /
	// configuration) via a typed URL; the detail mount redirects them to Apps once
	// the app loads, while a per-app manager (can_manage)
	// keeps access. Pin the gate so the viewer-only flow can't silently regress.
	assertContains(t, "views/app-detail-nav.js", "user.role === 'viewer' && !canManage",
		"resolveDetailAccess gates pure viewers out of the operator detail page (manager via can_manage keeps access); app-detail.js wires it, see TestAppDetailAccessResolverWired")
	assertContains(t, "app.js", "el.hidden = true",
		"view-only sessions omit the duplicate sidebar app catalog entirely")
}

// TestAppsNavigationConsolidationContract prevents the removed preview and its
// private API/query path from returning as a second Apps-like concept.
func TestAppsNavigationConsolidationContract(t *testing.T) {
	assertNotContains(t, "index.html", "Preview viewer home",
		"Overview must not carry the obsolete preview action")
	assertNotContains(t, "views/launchpad.js", "preview",
		"the viewer renderer must not retain a hidden preview mode")
	assertNotContains(t, "views/launchpad.js", "?as=viewer",
		"the viewer renderer must use the caller's real access scope")
}

// TestRootHomeUIContract pins the client wiring for the auth-aware root: the
// stable /home alias and logout landing on the contextual root.
func TestRootHomeUIContract(t *testing.T) {
	assertContains(t, "app.js", "router.register('/home'",
		"the SPA registers /home as the stable authenticated home alias")
	assertContains(t, "index.html", `href="/home" data-nav class="brand brand-home" aria-label="ShinyHub home"`,
		"the signed-in desktop and mobile brand marks must be accessible links to the stable home route")
	assertContains(t, "views/branding.js", "`ShinyHub — ${intent.siteTitle} home`",
		"signed-in home links must identify ShinyHub and the configured hub title")
	assertContains(t, "views/branding.js", "renderSignedInBrand(doc, slot, intent.siteTitle)",
		"signed-in branding must keep the ShinyHub lockup and render the hub title as a subtitle")
	assertContains(t, "style.css", ".brand-home:hover",
		"the brand home link must provide restrained pointer feedback")
	assertContains(t, "app.js", "router.register('/launchpad'",
		"the SPA preserves legacy Launchpad bookmarks as a compatibility redirect")
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
	assertContains(t, "views/launchpad-model.js", "emoji: appIconEmoji(app)",
		"the Launchpad tile model carries the emoji icon")
	assertContains(t, "views/launchpad.js", "renderAppAvatar",
		"the Launchpad tile renders the icon-or-monogram via the shared helper")
	assertContains(t, "views/launchpad.js", "emoji: t.emoji",
		"the Launchpad tile passes the emoji through to the shared avatar renderer")

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
	// The Remove button must stay visible for an emoji-only icon (no icon_mime),
	// not just an uploaded image.
	assertContains(t, "app.js", "!app.icon_mime && !app.icon_emoji",
		"the icon Remove button accounts for an emoji icon, not just an uploaded image")
}

// TestEmojiPickerUIContract pins the emoji-picker wiring added on top of
// TestAppIconUIContract: the picker reads the PATCH envelope correctly, and
// (per Task 9's review) the two pre-existing applyIconChange callers keep
// passing a literal empty emoji rather than re-asserting a stale one.
func TestEmojiPickerUIContract(t *testing.T) {
	// The set-emoji caller is the first caller to read a non-empty emoji back
	// from a PATCH response; IconEmoji is `json:"icon_emoji,omitempty"`, so a
	// cleared emoji is an absent key, not "" - reading body.app.icon_emoji
	// (with an || '' fallback) is the only correct way to unwrap it.
	assertContains(t, "app.js", "body.app.icon_emoji",
		"the emoji picker reads the new emoji from the wrapped PATCH response body.app, not a bare body.icon_emoji")

	// Carried forward from Task 9's review: pin both pre-existing
	// applyIconChange callers verbatim. Task 9 left their literal argument
	// shape unpinned because every caller passed emoji: '' at the time, so
	// undefined and '' read identically downstream. This task's set-emoji
	// caller is the first to pass a NON-empty emoji, which makes
	// `emoji: appIconEmoji(app)` a plausible copy-paste into the remove path
	// - and that one is silently wrong, since it would re-assert an emoji the
	// server has already cleared. removeIcon really does destroy both the
	// image and the emoji server-side, so a literal empty mime is correct
	// there; clearEmojiIcon does NOT share this literal (see the pin below) -
	// its clear path leaves icon_mime untouched server-side, so hardcoding
	// '' there would blank a retained image in the UI.
	assertContains(t, "app.js", "applyIconChange(app, { mime: '', emoji: '' })",
		"removeIcon must pass a literal empty emoji and mime, since it destroys both the image and the emoji")
	assertContains(t, "app.js", "applyIconChange(app, { mime: body.icon_mime || file.type, emoji: '' })",
		"uploadIcon must clear the emoji (a new image and an emoji are mutually exclusive), not re-assert a stale one")

	// Fix round 1: clearEmojiIcon's PATCH (icon_emoji: '') never touches
	// icon_mime server-side (SetAppIconEmoji writes icon_emoji alone), so an
	// app whose emoji coexisted with an uploaded/manifest image must keep
	// that image once the emoji is cleared. Hardcoding mime: '' here (as the
	// emoji-exclusive setEmojiIcon correctly does) blanked a retained image
	// in the detail page, sidebar, and grid until a reload. The fix reads the
	// server's own response for the authoritative mime.
	assertContains(t, "app.js", "body.app ? (body.app.icon_mime || '') : app.icon_mime",
		"clearEmojiIcon must read the authoritative icon_mime from the PATCH response (or keep the app's current value) instead of hardcoding an empty mime, or it blanks a retained image when only the emoji is cleared")

	// Markup + module wiring.
	assertContains(t, "index.html", `id="general-icon-emoji-btn"`,
		"Configuration > General has an emoji-picker trigger")
	assertContains(t, "index.html", `maxlength="32"`,
		"the emoji input's maxlength counts UTF-16 code units while the server's maxIconEmojiRunes counts runes, so it must stay at twice the 16-rune limit or a legitimate multi-rune sequence (a skin-toned family) silently truncates before the server ever sees it")
	assertContains(t, "index.html", `id="general-icon-emoji-popover"`,
		"the emoji picker is a popover, not a modal")
	assertContains(t, "app.js", "renderEmojiPicker(document",
		"app.js builds the emoji grid via the shared renderEmojiPicker module")
	assertContains(t, "app.js", "wireKebab(eBtn, ePopover",
		"the emoji picker reuses wireKebab for open/close instead of reimplementing it")

	// Live-surface fix: an uncomposable ZWJ sequence (a skin-toned
	// multi-person family, for example) has no single glyph in the local
	// font, so it renders as several glyphs side by side and overflows the
	// fixed-size avatar box, painting over the app name on the Launchpad
	// tile and over the h1 on the app-detail header. A bare
	// assertContains(t, "style.css", "overflow: hidden", ...) would be
	// vacuous here - that string already appears on .lp-tile-name and
	// .lp-tile-desc, so it would pass even with the emoji rule's
	// containment deleted. Locate the rule by its selector and assert
	// inside that block only, failing loudly if the selector is not found
	// so a rename cannot silently skip the check.
	{
		b, err := fs.ReadFile(ui.Static(), "style.css")
		if err != nil {
			t.Fatalf("read style.css: %v", err)
		}
		css := string(b)
		sel := ".lp-avatar--emoji, .app-detail-avatar--emoji, .icon-picker-preview--emoji {"
		start := strings.Index(css, sel)
		if start < 0 {
			t.Fatal("style.css: emoji-avatar variant rule not found by selector; a renamed selector must fail this test, not silently skip the containment check")
		}
		end := strings.Index(css[start:], "}")
		if end < 0 {
			t.Fatal("style.css: emoji-avatar variant rule has no closing brace")
		}
		block := css[start : start+end]
		if !strings.Contains(block, "overflow: hidden") {
			t.Fatal("style.css: the emoji-avatar variant rule must set overflow: hidden, or an uncomposable ZWJ sequence overflows the fixed-size avatar box and paints over adjacent text (the app name on the Launchpad tile, the page heading on the app-detail header)")
		}
	}
}

// TestCuratedEmojiValidatesAgainstServer is the cross-language guard the JS
// tests structurally cannot provide: a curated emoji the server rejects would
// render as a normal grid cell and 400 on click, and every JS test would
// still pass because JS never runs the Go validator. This reads the curated
// list out of the embedded views/emoji-picker.js and runs every entry through
// the real deploy.ValidateIconEmoji (Task 3), the same function the PATCH
// handler calls.
func TestCuratedEmojiValidatesAgainstServer(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "views/emoji-picker.js")
	if err != nil {
		t.Fatalf("read views/emoji-picker.js: %v", err)
	}
	src := string(b)

	literalRe := regexp.MustCompile(`\{\s*emoji:\s*'([^']*)'`)
	matches := literalRe.FindAllStringSubmatch(src, -1)
	// A regexp that matches nothing would make every assertion below pass
	// vacuously against an empty set - exactly the failure this test exists
	// to prevent. Fail loudly instead.
	if len(matches) == 0 {
		t.Fatal("extracted zero `emoji: '...'` literals from CURATED_EMOJI; the regexp no longer matches the source, which would otherwise let this test pass vacuously")
	}
	// entryCount is an independently-counted expectation (occurrences of the
	// entry-opening brace), so a literal the regexp cannot see (e.g. a
	// reformatted or multi-line entry) is a hard failure, not a silent skip.
	entryCount := strings.Count(src, "{ emoji:")
	if len(matches) != entryCount {
		t.Fatalf("regexp extracted %d emoji literals but CURATED_EMOJI has %d entries; a literal the regexp cannot see must fail, not be silently skipped", len(matches), entryCount)
	}

	seen := map[string]bool{}
	for _, m := range matches {
		emoji := m[1]
		if err := deploy.ValidateIconEmoji(emoji); err != nil {
			t.Errorf("CURATED_EMOJI entry %q fails deploy.ValidateIconEmoji: %v", emoji, err)
		}
		if seen[emoji] {
			t.Errorf("CURATED_EMOJI has a duplicate entry: %q", emoji)
		}
		seen[emoji] = true
	}
}

// TestAppDescriptionUIContract pins the Configuration > General description
// field to the PATCH it feeds (the same field the Launchpad renders).
func TestAppDescriptionUIContract(t *testing.T) {
	assertContains(t, "index.html", `id="general-description"`,
		"Configuration > General has a description field")
	assertContains(t, "app.js", "name, description, project_slug",
		"saveGeneralInfo PATCHes description alongside name + project_slug")
}

// TestAppDetailSurfacesLoadFailure guards UX-1: a non-OK, non-404, non-401
// response from GET /api/apps/:slug (e.g. a 500) must not be swallowed into a
// silent `return {}` that mounts a totally blank panel. mountAppDetail must
// throw so the router's error boundary (showRouteError in app.js) catches it
// and reveals #route-error-view with a Reload button instead.
func TestAppDetailSurfacesLoadFailure(t *testing.T) {
	assertNotContains(t, "views/app-detail.js", "if (!resp.ok) { return {}; }",
		"a failed GET /api/apps/:slug must not silently mount an empty panel with no error or retry")
	assertContains(t, "views/app-detail.js", "if (!resp.ok) { throw new Error(",
		"mountAppDetail must throw on a non-OK response so the router error boundary shows #route-error-view")
}

// TestSessionExpiryWarnsOnUnsavedChanges guards UX-2: handleUnauthorized used
// to wipe all client state unconditionally on a 401, discarding any unsaved
// settings edits with no warning. It must check anySettingsDirty() BEFORE
// showLoggedOut() clears state, and surface a message explaining the loss.
func TestSessionExpiryWarnsOnUnsavedChanges(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	src := string(b)
	start := strings.Index(src, "async function handleUnauthorized()")
	if start < 0 {
		t.Fatal("app.js: handleUnauthorized not found")
	}
	end := strings.Index(src[start:], "\n  }")
	if end < 0 {
		t.Fatal("app.js: could not find end of handleUnauthorized")
	}
	body := src[start : start+end]
	if !strings.Contains(body, "anySettingsDirty()") {
		t.Fatal("handleUnauthorized must call anySettingsDirty() to detect unsaved settings edits before wiping state")
	}
	dirtyCheck := strings.Index(body, "anySettingsDirty()")
	wipe := strings.Index(body, "showLoggedOut()")
	if wipe < 0 || dirtyCheck > wipe {
		t.Fatal("handleUnauthorized must check anySettingsDirty() BEFORE showLoggedOut() wipes client state")
	}
	if !strings.Contains(body, "Unsaved changes were lost") {
		t.Fatal("handleUnauthorized must warn the user that unsaved changes were lost on session expiry")
	}
}

// TestMetricsControllerOnErrorWired guards UX-3: the background metrics poll
// (createMetricsController) was constructed with only {intervalMs, onMetrics},
// so a failed poll (e.g. the session dying while the dashboard sat idle in a
// background tab) failed completely silently. onError must be wired, and a
// 401 specifically must log the user out via handleUnauthorized so a dead
// session doesn't keep polling forever with no visible signal. The
// onError/onMetrics contract itself is unit-tested in
// jstests/metrics-controller.test.js.
func TestMetricsControllerOnErrorWired(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	src := string(b)
	start := strings.Index(src, "const metrics = createMetricsController({")
	if start < 0 {
		t.Fatal("app.js: createMetricsController call site not found")
	}
	end := strings.Index(src[start:], "\n  });")
	if end < 0 {
		t.Fatal("app.js: could not find end of the createMetricsController call")
	}
	call := src[start : start+end]
	if !strings.Contains(call, "onError:") {
		t.Fatal("createMetricsController must be given an onError callback so a failed background poll is no longer silent")
	}
	if !strings.Contains(call, "handleUnauthorized()") {
		t.Fatal("the metrics onError callback must call handleUnauthorized() on a 401 so a dead session logs the user out instead of polling forever")
	}
}

// TestScheduleAndSharedDataHandlersHardened guards UX-4/UX-9: the schedule
// (run/delete/submit/history) and shared-data (mount/unmount) handlers used to
// call api() with no try/catch for network errors, no 401 check, and dumped
// the raw response body (a JSON {"error":"..."} envelope; see
// internal/api/helpers.go writeError) straight into a toast/inline error. They
// must match the established pattern used elsewhere in app.js: catch network
// failures, redirect a 401 through handleUnauthorized, and parse the .error
// field via the shared errorMessage helper instead of showing raw JSON.
func TestScheduleAndSharedDataHandlersHardened(t *testing.T) {
	assertContains(t, "app.js", "async function errorMessage(resp, fallback = 'Request failed')",
		"app.js must define a shared errorMessage helper that parses the {error} JSON envelope every API handler writes")

	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	src := string(b)

	checkHandler := func(startNeedle, label string) {
		t.Helper()
		start := strings.Index(src, startNeedle)
		if start < 0 {
			t.Fatalf("app.js: could not find %s (looked for %q)", label, startNeedle)
		}
		// Scan forward to the next top-level handler/function boundary so the
		// extracted body doesn't bleed into unrelated code; a generous window
		// comfortably covers each of these handlers.
		windowEnd := start + 2200
		if windowEnd > len(src) {
			windowEnd = len(src)
		}
		body := src[start:windowEnd]
		if !strings.Contains(body, "} catch {") {
			t.Fatalf("%s must catch a network failure from api(), not let it propagate uncaught", label)
		}
		if !strings.Contains(body, "r.status === 401") && !strings.Contains(body, "resp.status === 401") && !strings.Contains(body, "response.status === 401") {
			t.Fatalf("%s must check for a 401 and call handleUnauthorized()", label)
		}
		if !strings.Contains(body, "handleUnauthorized()") {
			t.Fatalf("%s must call handleUnauthorized() on a 401", label)
		}
		if !strings.Contains(body, "errorMessage(") {
			t.Fatalf("%s must parse the error via the shared errorMessage helper instead of showing the raw response body", label)
		}
	}

	checkHandler("async function runScheduleNow(slug, id, name, btn)", "runScheduleNow")
	checkHandler("async function deleteSchedule(slug, id, name, btn)", "deleteSchedule")
	checkHandler("async function cancelScheduleActivation(slug, schedID, name, btn)", "cancelScheduleActivation")
	checkHandler("container.querySelectorAll('[data-action=\"revoke\"]')", "the shared-data unmount handler")
	checkHandler("document.getElementById('shared-data-add-btn')", "the shared-data mount handler")

	historyStart := strings.Index(src, "async function loadScheduleRunPage(")
	if historyStart < 0 {
		t.Fatal("app.js: could not find paginated schedule run-history loader")
	}
	historyEnd := historyStart + 3200
	if historyEnd > len(src) {
		historyEnd = len(src)
	}
	historyBody := src[historyStart:historyEnd]
	for _, want := range []string{"} catch {", "response.status === 401", "handleUnauthorized()", "data-run-history-retry"} {
		if !strings.Contains(historyBody, want) {
			t.Fatalf("schedule run-history loader must contain %q for recoverable, authenticated reads", want)
		}
	}

	// The schedule add/edit submit handler is inside openScheduleForm; anchor on
	// its addEventListener('submit', ...) call specifically.
	submitStart := strings.Index(src, "newForm.addEventListener('submit', async e => {")
	if submitStart < 0 {
		t.Fatal("app.js: could not find the schedule form submit handler")
	}
	// Keep enough room for the complete payload assembly as schedule policy
	// controls grow; the boundary is still well inside openScheduleForm.
	submitEnd := submitStart + 3000
	if submitEnd > len(src) {
		submitEnd = len(src)
	}
	submitBody := src[submitStart:submitEnd]
	if !strings.Contains(submitBody, "} catch {") {
		t.Fatal("the schedule form submit handler must catch a network failure from api()")
	}
	if !strings.Contains(submitBody, "r.status === 401") || !strings.Contains(submitBody, "handleUnauthorized()") {
		t.Fatal("the schedule form submit handler must check for a 401 and call handleUnauthorized()")
	}
	if !strings.Contains(submitBody, "errorMessage(") {
		t.Fatal("the schedule form submit handler must parse the error via errorMessage instead of showing the raw response body")
	}
}

// TestLoginHandlesRateLimitAndDoubleSubmit guards UX-6/UX-8: a 429 from the
// rate-limited login endpoint used to fall through to the generic "Login
// failed" message, giving no hint that retrying immediately won't help; and
// the submit button had no disabled-during-request guard, so a slow request
// (or an impatient double-click) could fire the login POST twice.
func TestLoginHandlesRateLimitAndDoubleSubmit(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	src := string(b)
	start := strings.Index(src, "loginForm.addEventListener('submit'")
	if start < 0 {
		t.Fatal("app.js: login form submit handler not found")
	}
	end := strings.Index(src[start:], "\n  });")
	if end < 0 {
		t.Fatal("app.js: could not find end of the login submit handler")
	}
	body := src[start : start+end]
	if !strings.Contains(body, "response.status === 429") {
		t.Fatal("the login submit handler must special-case a 429 (rate limited) response")
	}
	if !strings.Contains(body, "Too many attempts") {
		t.Fatal("a 429 login response must show a message distinct from the generic 'Login failed', so the user knows to wait rather than retry the same credentials")
	}
	if !strings.Contains(body, "submitBtn.disabled = true") || !strings.Contains(body, "submitBtn.disabled = false") {
		t.Fatal("the login submit button must be disabled for the duration of the request to prevent a double-submit")
	}
	if !strings.Contains(body, "Signing in…") || !strings.Contains(body, "aria-busy") {
		t.Fatal("the login submit button must name and expose its in-progress state while authentication is pending")
	}
}

// TestSchedulesTableResponsiveCards guards the schedule workspace's mobile
// contract: operational rows reflow into labelled cards instead of requiring
// horizontal scrolling, which hides state and actions off-screen.
func TestSchedulesTableResponsiveCards(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	css := string(b)
	for _, want := range []string{
		".schedules-table thead",
		".schedules-table td::before",
		"content: attr(data-label)",
		"grid-template-columns: 6.8rem minmax(0, 1fr)",
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("style.css: responsive schedules contract missing %q", want)
		}
	}
}

// TestEscapeClosesNewTokenModal guards UX-7: the global Escape key handler
// closed every other modal (deploy/new-app/new-user/profile/reset-password/
// schedule) and the log pane, but omitted the new-API-token modal. That modal
// reveals a bearer secret once (tokenRevealValue); leaving it out of the
// Escape chain meant a reflexive Escape press did nothing, unlike every other
// modal in the app.
func TestEscapeClosesNewTokenModal(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	src := string(b)
	start := strings.Index(src, "document.addEventListener('keydown', e => {")
	if start < 0 {
		t.Fatal("app.js: global keydown handler not found")
	}
	end := strings.Index(src[start:], "\n  });")
	if end < 0 {
		t.Fatal("app.js: could not find end of the global keydown handler")
	}
	body := src[start : start+end]
	if !strings.Contains(body, "newTokenModal && !newTokenModal.hidden") {
		t.Fatal("the global Escape handler must branch on the new-token modal being open")
	}
	if !strings.Contains(body, "closeNewTokenModal()") {
		t.Fatal("the global Escape handler must call closeNewTokenModal() so the revealed secret doesn't linger visible")
	}
}

func TestSupportSessionModalCannotDismissWhileCreationIsPending(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	src := string(b)
	for _, want := range []string{
		"createSupportSessionModalLock",
		"supportModalLock.setPending(true);",
		"supportModalLock.setPending(false);",
		"supportModalLock.requestDismiss",
		"supportModal && !supportModal.hidden",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("app.js: support-session modal safety contract missing %q", want)
		}
	}
}

// TestDoubleSubmitGuardsOnDestructiveActions guards the rest of UX-8: several
// destructive/mutating actions (delete user, revoke token, create user, card
// restart) had no disabled-during-request guard, so a slow request or an
// impatient double-click could fire the request twice. They must all follow
// the same disable-before/re-enable-after pattern already used by the login
// and schedule handlers. Card-local restart confirmation has no persistent
// trigger button during the request, so it also guards on its pending model.
func TestDoubleSubmitGuardsOnDestructiveActions(t *testing.T) {
	b, err := fs.ReadFile(ui.Static(), "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	src := string(b)

	checkGuard := func(startNeedle, label, disableExpr, enableExpr string) {
		t.Helper()
		start := strings.Index(src, startNeedle)
		if start < 0 {
			t.Fatalf("app.js: could not find %s (looked for %q)", label, startNeedle)
		}
		windowEnd := start + 1200
		if windowEnd > len(src) {
			windowEnd = len(src)
		}
		body := src[start:windowEnd]
		if !strings.Contains(body, disableExpr) || !strings.Contains(body, enableExpr) {
			t.Fatalf("%s must disable its trigger button for the duration of the request (expected %q and %q)", label, disableExpr, enableExpr)
		}
	}

	checkGuard("async function deleteUser(id, username, btn)", "deleteUser", "btn.disabled = true", "btn.disabled = false")
	checkGuard("async function revokeToken(id, name, btn)", "revokeToken", "btn.disabled = true", "btn.disabled = false")
	checkGuard("async function submitNewUser(event)", "submitNewUser", "submitBtn.disabled = true", "submitBtn.disabled = false")
	checkGuard("async function performRestart(slug, btn, cardLocal = false)", "the Restart request handler", "btn.disabled = true", "btn.disabled = false")
	checkGuard("async function lifecycleAction(slug, path, btn, failureMessage)", "the Sleep/Stop/Start handler", "btn.disabled = true", "btn.disabled = false")
	if !strings.Contains(src, "appRestartFeedback.get(slug)?.phase === 'pending'") {
		t.Fatal("card-local Restart must reject a second request while its first restart is pending")
	}

	// Each guarded function must actually receive the triggering button at its
	// call site, not just declare an unused parameter.
	if !strings.Contains(src, "deleteUser(u.id, u.username, delBtn)") {
		t.Fatal("the Delete user button click handler must pass its own button through to deleteUser for the disable guard")
	}
	if !strings.Contains(src, "revokeToken(btn.getAttribute('data-token-id'), btn.getAttribute('data-token-name'), btn)") {
		t.Fatal("the Revoke token button click handler must pass its own button through to revokeToken for the disable guard")
	}
	// Restart, Sleep, Stop and Start are wired from one table in the card kebab,
	// so a single call site carries the button through for all four. Dropping the
	// argument there would remove the guard from every one of them at once.
	if !strings.Contains(src, "handler(app.slug, e.currentTarget)") {
		t.Fatal("each card lifecycle button (Restart, Sleep, Stop, Start) must pass its own button through for the disable guard")
	}
	// Each lifecycle handler must accept that button, or the shared call site
	// above would hand it to a function that ignores it.
	for _, sig := range []string{
		"async function sleepApp(slug, btn)",
		"async function stopApp(slug, btn)",
		"async function startApp(slug, btn)",
	} {
		if !strings.Contains(src, sig) {
			t.Fatalf("expected %q so the lifecycle handler can disable its trigger button", sig)
		}
	}
}

// TestRollbackFailureUsesToastNotAlert guards UX-9: a failed rollback (network
// error or a non-OK response) used window.alert(), a blocking, unstyled
// browser dialog inconsistent with every other error surface in the
// dashboard. It must report through the same accessible flashToast used
// elsewhere, which app.js exposes on ctx for exactly this purpose.
func TestRollbackFailureUsesToastNotAlert(t *testing.T) {
	assertContains(t, "app.js", "flashToast,",
		"app.js must expose flashToast on the ctx object passed to mountAppDetail so app-detail.js can report failures without window.alert()")
	assertNotContains(t, "views/app-detail.js", "alert(",
		"app-detail.js must not use window.alert() for rollback failures; use ctx.flashToast so failures match the rest of the dashboard's error UI")
	assertContains(t, "views/app-detail.js", "ctx.flashToast('Rollback failed: network error.', 'error')",
		"a network failure during rollback must be reported via ctx.flashToast, not a blocking alert()")
	assertContains(t, "views/app-detail.js", "ctx.flashToast(msg, 'error')",
		"a non-OK rollback response must be reported via ctx.flashToast, not a blocking alert()")
}

// The grid grouping decision lives in a unit-tested module, and app.js only
// calls it. jsdom cannot import the app.js IIFE, so these string assertions are
// the only thing standing between "app-grid-groups.test.js is green" and "the
// dashboard still renders one flat list".
func TestGridGroupsByProject(t *testing.T) {
	assertContains(t, "app.js", "groupAppsForGrid",
		"renderApps must group through the unit-tested groupAppsForGrid helper, not inline")
	assertContains(t, "app.js", "views/app-grid-groups.js",
		"app.js must import the grid grouper module")
	assertContains(t, "app.js", "createGroupDisclosure",
		"renderGridVerbatim must emit a project disclosure, or grouping is computed and thrown away")
	assertContains(t, "app.js", "classPrefix: 'app-grid'",
		"the grid disclosure must use the app-grid component vocabulary")
	assertContains(t, "views/apps-grid.js", "groupAppsForGrid",
		"the apps-grid fallback render path must group too, or a mount without applyGridFilters renders flat")
	// The sort must reach the grouper rather than being applied across the whole
	// list first; sorting before grouping silently makes the sort a no-op at the
	// group level and reorders nothing the user can see.
	assertContains(t, "app.js", "groupAppsForGrid(apps, { sortKey",
		"renderApps must pass the selected sort key into the grouper so it applies within each group")
	assertNotContains(t, "app.js", "apps.sort((a, b) => a.name.localeCompare(b.name))",
		"the in-group name comparator must live in app-grid-groups.js, not be duplicated in renderApps")
}

// Each disclosure is a full-width section. Its body, rather than the outer
// section list, owns the responsive card grid so cards cannot sit beside their
// project heading.
func TestGridGroupBodyOwnsTheCardGrid(t *testing.T) {
	assertContains(t, "style.css", ".app-grid-group-heading",
		"style.css must style the grid group heading")
	assertContains(t, "style.css", ".app-grid-group-body",
		"style.css must give each project disclosure its own card-grid body")
	assertContains(t, "style.css", "grid-template-columns: repeat(auto-fill, minmax(340px, 1fr))",
		"project disclosure bodies must retain the responsive card grid")
}

// readStatic returns an embedded asset as a string. The rest of this file
// inlines this two-liner; the project tests read the same file repeatedly, so
// it is worth a name.
func readStatic(t *testing.T, path string) string {
	t.Helper()
	b, err := fs.ReadFile(ui.Static(), path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// inputTagByID returns the single <input> tag carrying id="<id>", so a per-input
// assertion cannot be satisfied by a DIFFERENT input elsewhere in the file.
// Asserting on the whole document would pass whenever either input had the
// attribute, which is exactly the failure these tests exist to catch.
func inputTagByID(t *testing.T, html, id string) string {
	t.Helper()
	needle := `id="` + id + `"`
	i := strings.Index(html, needle)
	if i < 0 {
		t.Fatalf("no element with %s in index.html", needle)
	}
	start := strings.LastIndex(html[:i], "<input")
	if start < 0 {
		t.Fatalf("%s is not inside an <input> tag", needle)
	}
	end := strings.Index(html[start:], ">")
	if end < 0 {
		t.Fatalf("unterminated <input> tag for %s", needle)
	}
	return html[start : start+end+1]
}

// jsFunctionBody returns the source text of one function declaration inside a
// static JS asset, located by the literal "function <name>(" and extended by
// brace-matching from the first "{" to its balancing "}". Brace-matching -
// not a line count or "until the next blank line" heuristic - is what makes
// this survive the function moving or being reformatted: a heuristic would
// silently return the wrong slice when that happens, where this fails loudly
// instead. Used to scope an assertion to one function's body, so a call it
// must make cannot be satisfied by an unrelated reference (e.g. a dangling
// import) anywhere else in the file.
func jsFunctionBody(t *testing.T, js, name string) string {
	t.Helper()
	needle := "function " + name + "("
	start := strings.Index(js, needle)
	if start < 0 {
		t.Fatalf("no function named %s in the asset", name)
	}
	relBrace := strings.Index(js[start:], "{")
	if relBrace < 0 {
		t.Fatalf("function %s has no opening brace", name)
	}
	braceStart := start + relBrace
	depth := 0
	for i := braceStart; i < len(js); i++ {
		switch js[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return js[start : i+1]
			}
		}
	}
	t.Fatalf("function %s: braces do not balance", name)
	return ""
}

// Both project inputs, asserted one at a time. A single assertion that passes
// on either would let the New app modal - the input where a typo actually
// creates a duplicate project - ship without autocomplete.
func TestBothProjectInputsShareTheProjectDatalist(t *testing.T) {
	html := readStatic(t, "index.html")
	for _, id := range []string{"general-project", "new-app-project"} {
		input := inputTagByID(t, html, id)
		if !strings.Contains(input, `list="project-slugs"`) {
			t.Errorf("#%s must reference the shared project datalist, got: %s", id, input)
		}
		if !strings.Contains(input, `maxlength="63"`) {
			t.Errorf("#%s must cap input at the 63-char slug limit, got: %s", id, input)
		}
		// The literal slug.Pattern, copied not re-derived: slug.go says this
		// string is kept in sync with the SPA.
		if !strings.Contains(input, `pattern="[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?"`) {
			t.Errorf("#%s must carry the slug pattern, got: %s", id, input)
		}
	}
	if !strings.Contains(html, `<datalist id="project-slugs"`) {
		t.Error("index.html must define the shared project datalist exactly once")
	}
	if strings.Count(html, `<datalist id="project-slugs"`) != 1 {
		t.Error("the project datalist must be a single shared element, not one per input")
	}
}

// The first-app path is an activation flow, not a resource-creation dead end:
// after the app row is created the same modal must advance to deployment and
// offer the browser uploader immediately. These source-level wiring assertions
// complement the document-wide accessibility test without coupling to styling.
func TestFirstAppOnboardingAdvancesDirectlyToDeploy(t *testing.T) {
	html := readStatic(t, "index.html")
	for _, want := range []string{
		`id="empty-state-cta" class="emptystate-btn emptystate-btn-primary">Deploy your first app`,
		`id="new-app-step"`,
		`id="new-app-deploy-now"`,
		`Step 1 of 2`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("first-app onboarding markup missing %q", want)
		}
	}

	js := readStatic(t, "app.js")
	showHandoff := jsFunctionBody(t, js, "showNewAppHandoff")
	for _, want := range []string{"Step 2 of 2", "newAppDeployNow", "app.slug"} {
		if !strings.Contains(showHandoff, want) {
			t.Errorf("showNewAppHandoff must advance the real app into deploy step; missing %q", want)
		}
	}
	if !strings.Contains(js, "closeNewAppModal();\n    openDeployModal(app);") {
		t.Error("Upload app must close the creation modal and open the browser deploy modal")
	}
}

// Remote CLI onboarding is initiated in the terminal and approved in the
// signed-in dashboard. The browser receives only a SHA-256 hash, never the raw
// credential, and the request survives SSO redirects in per-tab storage.
func TestRemoteCLIConnectionOnboardingContract(t *testing.T) {
	html := readStatic(t, "index.html")
	for _, want := range []string{
		`id="cli-connect-panel"`, `id="cli-connect-code"`,
		`id="cli-connect-approve"`, `id="cli-connect-success"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("CLI connection onboarding markup missing %q", want)
		}
	}
	js := readStatic(t, "app.js")
	for _, want := range []string{
		"sessionStorage.setItem(CLI_CONNECT_STORAGE_KEY",
		"/api/tokens/connect",
		"token_hash: request.tokenHash",
		"restoreCLIConnectRoute()",
		"shinyhub connect ${origin}",
		"shinyhub deploy . --slug ${slug} --wait",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("remote CLI onboarding wiring missing %q", want)
		}
	}
	if strings.Contains(js, "connect_hash: raw") || strings.Contains(js, "connect_token") {
		t.Fatal("browser pairing must never carry the raw CLI credential")
	}
}

// Scoped per input on purpose: the document already contains this pattern on
// #new-app-slug, so a document-wide search passes without either project input
// carrying it.
func TestProjectInputPatternMatchesSlugPattern(t *testing.T) {
	html := readStatic(t, "index.html")
	want := `pattern="` + slugpkg.Pattern + `"`
	for _, id := range []string{"general-project", "new-app-project"} {
		if input := inputTagByID(t, html, id); !strings.Contains(input, want) {
			t.Errorf("#%s must carry the literal slug.Pattern %q, got: %s", id, slugpkg.Pattern, input)
		}
	}
}

func TestProjectEditModalWiring(t *testing.T) {
	assertContains(t, "index.html", `id="project-edit-modal"`,
		"index.html must define the project edit modal")
	assertContains(t, "app.js", "/api/projects/",
		"app.js must PATCH the project endpoint from the edit modal")
	assertContains(t, "app.js", "app-grid-group-edit",
		"the group heading must carry the edit control that opens the modal")
	assertContains(t, "app.js", "edit.setAttribute('aria-label', `Edit project ${group.name}`)",
		"the compact icon-only edit control must retain an accessible name")
	assertContains(t, "app.js", "editIcon.setAttribute('aria-hidden', 'true')",
		"the decorative pencil icon must be hidden from assistive technology")
	assertContains(t, "app.js", "populateProjectDatalist",
		"app.js must populate the shared datalist from GET /api/projects")
}

// The edit modal must not send an empty description it never loaded.
// group objects come from groupApps(), which carries no description field, so
// an unconditional description key turns "open Edit, press Save" into
// "delete the description" (internal/api/projects.go treats "" as an explicit
// clear, nil as absent).
//
// This property is covered by two independent layers, and each check below
// is labeled with which one it is:
//
//   - jstests/project-edit-body.test.js unit-tests buildProjectPatchBody's
//     own behavior (does it include or omit the key). That is the layer that
//     actually holds against a source-shape rewrite of the same bug - a
//     reviewer defeated an earlier version of the check below by rewriting
//     an unconditional-description body with assignment syntax instead of an
//     object literal.
//   - The checks in this test are a cheap, string-search-based second layer.
//     They cannot verify behavior, only that saveProjectEdit still calls
//     into the tested function rather than reconstructing the PATCH body
//     itself. An earlier version of that call-site check asserted
//     `buildProjectPatchBody` appeared anywhere in app.js, which a dangling,
//     unused import satisfies even after saveProjectEdit stops calling it
//     (this repo has no JS linter to flag the dead import). Scoping the
//     assertion to saveProjectEdit's own function body via jsFunctionBody
//     closes that hole: the string must appear where the call would actually
//     be made, not merely somewhere in the file.
func TestProjectEditModalDoesNotClearDescription(t *testing.T) {
	appJS := readStatic(t, "app.js")
	saveFn := jsFunctionBody(t, appJS, "saveProjectEdit")
	if !strings.Contains(saveFn, "buildProjectPatchBody(") {
		t.Error("saveProjectEdit must build its PATCH body by calling buildProjectPatchBody, not by reconstructing the body inline")
	}
	assertContains(t, "app.js", "views/project-edit-body.js",
		"app.js must import buildProjectPatchBody from views/project-edit-body.js")
	assertContains(t, "app.js", "descriptionKnown",
		"saveProjectEdit must set/read the descriptionKnown flag it forwards to buildProjectPatchBody")

	body := readStatic(t, "views/project-edit-body.js")
	if !strings.Contains(body, "descriptionKnown") {
		t.Error("buildProjectPatchBody must gate the description key on descriptionKnown")
	}
	if strings.Contains(body, "icon_emoji: iconEmoji, description") {
		t.Error("buildProjectPatchBody must never include description unconditionally in the initial object literal: \"\" is an explicit clear")
	}
}

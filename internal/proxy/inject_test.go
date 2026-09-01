package proxy

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/appnav"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/favicon"
	"github.com/rvben/shinyhub/internal/supportui"
)

// overlayOnly is the injection set for a proxy with just the status overlay
// enabled, which is what most of these tests exercise.
func overlayOnly(slug string) func(*http.Request) []pageScript {
	return func(*http.Request) []pageScript { return []pageScript{overlayPageScript(slug)} }
}

// appPageLoad is the request shape ServeHTTP forwards for a top-level browser
// navigation to slug. It goes through render_gate_test.go's pageLoadRequest so
// both files agree on what a page load looks like.
func appPageLoad(slug string) *http.Request {
	return pageLoadRequest("/app/" + slug + "/")
}

// htmlResponse builds a backend response carrying body as an HTML page load.
func htmlResponse(slug, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":   []string{"text/html; charset=utf-8"},
			"Content-Length": []string{strconv.Itoa(len(body))},
		},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       appPageLoad(slug),
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// The shell a Shiny app actually serves: the marker is a node the app owns, so
// asserting the snippet lands after it AND before </body> bounds the insertion
// point on both sides. A one-sided check ("the snippet is present") passes when
// the snippet is spliced into the middle of the app's own markup.
const testShell = `<!DOCTYPE html><html><head><title>App</title></head>` +
	`<body><div id="app-root">content</div></body></html>`

func TestOverlayCSPHash_MatchesEmbeddedScript(t *testing.T) {
	// The hash is what a strict CSP checks the served bytes against. If it is
	// computed from anything but the embedded script, every app with a CSP
	// silently refuses to run the overlay and there is no other signal.
	sum := sha256.Sum256([]byte(overlayScript))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if overlayCSPHash != want {
		t.Fatalf("overlayCSPHash = %s, want %s", overlayCSPHash, want)
	}

	// And the hash covers the script body exactly: the snippet must place the
	// hashed bytes between the tags, with nothing appended inside them.
	snippet := overlaySnippet("demo")
	openEnd := strings.Index(snippet, ">")
	if openEnd < 0 {
		t.Fatalf("snippet has no opening tag: %q", snippet)
	}
	body := snippet[openEnd+1 : len(snippet)-len("</script>")]
	if body != overlayScript {
		t.Fatalf("snippet body differs from the hashed script (%d vs %d bytes)", len(body), len(overlayScript))
	}
}

func TestOverlaySnippet_PerAppValuesRideOnAttributes(t *testing.T) {
	// One CSP hash admits the overlay fleet-wide only while the script text is
	// byte-identical for every app. Anything per-app must be an attribute.
	a := overlaySnippet("alpha")
	b := overlaySnippet("beta")
	if strings.Contains(overlayScript, "alpha") || strings.Contains(overlayScript, "/app/") {
		t.Fatal("overlay script embeds a per-app value; it must read one from data-*")
	}
	if !strings.Contains(a, `data-ready-url="/app/alpha`+readySuffix+`"`) {
		t.Fatalf("alpha snippet lacks its ready URL: %s", a[:min(200, len(a))])
	}
	if !strings.Contains(b, `data-ready-url="/app/beta`+readySuffix+`"`) {
		t.Fatalf("beta snippet lacks its ready URL: %s", b[:min(200, len(b))])
	}
	// The differing part must be attributes only, so stripping the tags leaves
	// two identical bodies.
	if strings.TrimPrefix(a, a[:strings.Index(a, ">")]) != strings.TrimPrefix(b, b[:strings.Index(b, ">")]) {
		t.Fatal("snippet bodies differ between apps; the CSP hash would only match one")
	}
}

func TestOverlaySnippet_EscapesSlugIntoTheAttribute(t *testing.T) {
	// Slugs are validated elsewhere, so this is defence in depth rather than a
	// live hole: a slug that could close the attribute would let an app author
	// inject markup into every page ShinyHub serves for it.
	got := overlaySnippet(`x" onload="evil()`)
	if strings.Contains(got, `onload="evil()`) {
		t.Fatalf("slug escaped its attribute: %s", got[:min(200, len(got))])
	}
	if !strings.Contains(got, "&#34;") {
		t.Fatalf("quote was not escaped: %s", got[:min(200, len(got))])
	}
}

func TestExtendCSPForScripts(t *testing.T) {
	h := overlayCSPHash
	cases := []struct {
		name   string
		policy string
		want   string
		ok     bool
	}{
		{"no policy at all", "", "", true},
		{"whitespace only", "   ", "   ", true},
		{"script-src gains the hash", "script-src 'self'", "script-src 'self' " + h, true},
		{"script-src is matched case-insensitively", "Script-Src 'self'", "Script-Src 'self' " + h, true},
		{
			"other directives are untouched",
			"default-src 'none'; script-src 'self'; img-src *",
			"default-src 'none'; script-src 'self' " + h + "; img-src *",
			true,
		},
		{
			"no script-src promotes default-src",
			"default-src 'self' https:",
			"default-src 'self' https:; script-src 'self' https: " + h,
			true,
		},
		{"trailing semicolon does not double up", "default-src 'self';", "default-src 'self'; script-src 'self' " + h, true},
		{"already present is a no-op", "script-src 'self' " + h, "script-src 'self' " + h, true},
		{"unrelated directives need no change", "img-src 'self'; frame-ancestors 'none'", "img-src 'self'; frame-ancestors 'none'", true},
		// The declines. Each is a policy whose author said "no scripts", which
		// this proxy must not talk itself out of.
		{"script-src 'none' declines", "default-src 'self'; script-src 'none'", "", false},
		{"default-src 'none' declines", "default-src 'none'", "", false},
		{"valueless default-src declines", "default-src", "", false},
		{"duplicate script-src declines", "script-src 'self'; script-src 'none'", "", false},
		{"duplicate script-src-elem declines", "script-src-elem 'self'; script-src-elem 'none'", "", false},
		{"duplicate default-src declines", "default-src 'self'; default-src 'none'", "", false},
		{"tab-separated duplicate declines", "script-src\t'self'; script-src\t'none'", "", false},
		{"comma-separated policy list declines", "script-src 'self', script-src 'none'", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := extendCSPForScripts(tc.policy, []string{h})
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (got policy %q)", ok, tc.ok, got)
			}
			if !ok {
				return
			}
			if got != tc.want {
				t.Fatalf("policy =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

func TestExtendCSPForScripts_NeverWidens(t *testing.T) {
	// The one thing this function must never do, stated as its own test so a
	// future "just allow inline, it is simpler" cannot pass review silently.
	//
	// Both hashes go in together, which is the real shape once the overlay and
	// the app switcher are both enabled: the count assertion below then also
	// pins that extending for two scripts adds two sources and not a wildcard
	// standing in for them.
	hashes := []string{overlayCSPHash, appnav.CSPHash}
	for _, policy := range []string{
		"script-src 'self'",
		"default-src 'self'",
		"default-src 'none'; script-src 'self'",
		"script-src 'strict-dynamic' 'nonce-abc'",
	} {
		got, ok := extendCSPForScripts(policy, hashes)
		if !ok {
			continue
		}
		for _, forbidden := range []string{"'unsafe-inline'", "'unsafe-eval'", "*"} {
			if strings.Contains(got, forbidden) && !strings.Contains(policy, forbidden) {
				t.Fatalf("extending %q introduced %s: %q", policy, forbidden, got)
			}
		}
		added := strings.Count(got, "'sha256-")
		if added != strings.Count(policy, "'sha256-")+len(hashes) {
			t.Fatalf("extending %q should add exactly %d hashes, got %q", policy, len(hashes), got)
		}
	}
}

func TestExtendCSPForScripts_TwoScriptsBothAdmitted(t *testing.T) {
	// A page carrying both injections needs both hashes in the same directive.
	// Admitting only the first would leave the app switcher blocked on every
	// CSP-enforcing app, visible as nothing at all rather than as an error.
	got, ok := extendCSPForScripts("script-src 'self'", []string{overlayCSPHash, appnav.CSPHash})
	if !ok {
		t.Fatal("extension declined a policy it can narrow")
	}
	if !strings.Contains(got, overlayCSPHash) {
		t.Fatalf("overlay hash absent: %q", got)
	}
	if !strings.Contains(got, appnav.CSPHash) {
		t.Fatalf("app switcher hash absent: %q", got)
	}
	if overlayCSPHash == appnav.CSPHash {
		t.Fatal("the two scripts hash identically, so this test proves nothing")
	}
}

func TestExtendCSPForScripts_NoHashesIsANoOp(t *testing.T) {
	// Both injections off: the policy must come back untouched rather than
	// gaining an empty script-src promoted from default-src.
	const policy = "default-src 'self' https:"
	got, ok := extendCSPForScripts(policy, nil)
	if !ok {
		t.Fatal("extension declined with nothing to add")
	}
	if got != policy {
		t.Fatalf("policy = %q, want it unchanged (%q)", got, policy)
	}
}

func TestInjectableResponse(t *testing.T) {
	subresource := httptest.NewRequest(http.MethodGet, "/app/demo/style.css", nil)
	subresource.Header.Set("Sec-Fetch-Dest", "style")

	cases := []struct {
		name string
		mut  func(*http.Response)
		want bool
	}{
		{"html page load", func(*http.Response) {}, true},
		{"charset-less content type", func(r *http.Response) {
			r.Header.Set("Content-Type", "text/html")
		}, true},
		{"identity encoding is still plain", func(r *http.Response) {
			r.Header.Set("Content-Encoding", "identity")
		}, true},
		{"not 200", func(r *http.Response) { r.StatusCode = http.StatusNotFound }, false},
		{"redirect", func(r *http.Response) { r.StatusCode = http.StatusFound }, false},
		{"sub-resource", func(r *http.Response) { r.Request = subresource }, false},
		{"json", func(r *http.Response) { r.Header.Set("Content-Type", "application/json") }, false},
		{"compressed", func(r *http.Response) { r.Header.Set("Content-Encoding", "gzip") }, false},
		{"no request", func(r *http.Response) { r.Request = nil }, false},
		{"no body", func(r *http.Response) { r.Body = nil }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := htmlResponse("demo", testShell)
			tc.mut(resp)
			if got := injectableResponse(resp); got != tc.want {
				t.Fatalf("injectableResponse = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInjectStatusOverlay_RewritesThePageAndItsHeaders(t *testing.T) {
	resp := htmlResponse("demo", testShell)
	resp.Header.Set("ETag", `"abc123"`)
	resp.Header.Set("Content-Security-Policy", "default-src 'self'")

	if err := injectPageScripts(overlayOnly("demo"))(resp); err != nil {
		t.Fatalf("inject: %v", err)
	}

	body := readBody(t, resp)
	if !strings.Contains(body, `data-ready-url="/app/demo`+readySuffix+`"`) {
		t.Fatal("overlay snippet missing from the rewritten page")
	}
	if idx, closing := strings.Index(body, "shinyhub-status-overlay-loader"), strings.LastIndex(body, "</body>"); !(idx > 0 && idx < closing) {
		t.Fatalf("snippet at %d is not inside the body (closing tag at %d)", idx, closing)
	}
	if got := resp.Header.Get("Content-Security-Policy"); !strings.Contains(got, overlayCSPHash) {
		t.Fatalf("CSP not extended for the injected script: %q", got)
	}
	if resp.Header.Get("ETag") != "" {
		t.Fatal("ETag survived a body rewrite; caches would serve the old bytes for the new page")
	}
	if got, want := resp.Header.Get("Content-Length"), strconv.Itoa(len(body)); got != want {
		t.Fatalf("Content-Length = %s, want %s (the rewritten length)", got, want)
	}
	if resp.ContentLength != int64(len(body)) {
		t.Fatalf("resp.ContentLength = %d, want %d", resp.ContentLength, len(body))
	}
}

func TestSupportSessionBannerIsInjectedAndCSPBound(t *testing.T) {
	p := New()
	p.SetSupportSessions(true)
	p.SetAppNav(true, "https://hub.example.com/")
	resp := htmlResponse("sales", testShell)
	resp.Request = resp.Request.WithContext(auth.WithUser(resp.Request.Context(), &auth.ContextUser{
		ID: 22, Username: "alice", Role: "viewer",
		SupportSession: &auth.SupportSessionContext{
			ID: "support-id", ActorID: 11, ActorUsername: "admin", AppSlug: "sales",
			ExpiresAt: time.Now().Add(15 * time.Minute),
		},
	}))
	resp.Header.Set("Content-Security-Policy", "default-src 'self'")
	if err := p.modifyResponseFor("sales")(resp); err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	for _, want := range []string{"shinyhub-support-session-loader", `data-actor="admin"`, `data-subject="alice"`, "End support session"} {
		if !strings.Contains(body, want) {
			t.Fatalf("support banner missing %q", want)
		}
	}
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), supportui.CSPHash) {
		t.Fatalf("CSP missing support script hash: %q", resp.Header.Get("Content-Security-Policy"))
	}
	if strings.Contains(body, "shinyhub-app-nav") {
		t.Fatal("app switcher must be suppressed while the single-app support boundary is active")
	}
}

func TestSupportSessionFailsClosedWhenBannerCannotRun(t *testing.T) {
	p := New()
	p.SetSupportSessions(true)
	resp := htmlResponse("sales", `<html><body><p id="private-app">sensitive app</p></body></html>`)
	resp.Request = resp.Request.WithContext(auth.WithUser(resp.Request.Context(), &auth.ContextUser{
		ID: 22, Username: "alice", Role: "viewer",
		SupportSession: &auth.SupportSessionContext{
			ID: "support-id", ActorID: 11, ActorUsername: "admin", AppSlug: "sales", ExpiresAt: time.Now().Add(15 * time.Minute),
		},
	}))
	resp.Header.Set("Content-Security-Policy", "script-src 'none'")
	if err := p.modifyResponseFor("sales")(resp); err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "Support session paused") ||
		!strings.Contains(body, "End support session") || strings.Contains(body, "sensitive app") {
		t.Fatalf("fail-closed response status=%d body=%s", resp.StatusCode, body)
	}
}

func TestSupportSessionCSPElementDirectiveAndMetaFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy string
		body   string
	}{
		{name: "script element denied", policy: "script-src 'self'; script-src-elem 'none'", body: testShell},
		{name: "form action denied", policy: "script-src 'self'; form-action 'none'", body: testShell},
		{name: "comma list form action denied", policy: "script-src 'self', form-action 'none'", body: testShell},
		{name: "comma list script denied", policy: "script-src 'self', script-src 'none'", body: testShell},
		{name: "sandbox denies forms", policy: "script-src 'self'; sandbox allow-scripts", body: testShell},
		{name: "sandbox opaque origin omits cookie", policy: "script-src 'self'; sandbox allow-scripts allow-forms", body: testShell},
		{name: "meta policy", body: `<html><head><meta http-equiv="Content-Security-Policy" content="script-src 'none'"></head><body>sensitive</body></html>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := New()
			p.SetSupportSessions(true)
			resp := htmlResponse("sales", tc.body)
			resp.Request = resp.Request.WithContext(auth.WithUser(resp.Request.Context(), &auth.ContextUser{
				ID: 22, Username: "alice", Role: "viewer", SupportSession: &auth.SupportSessionContext{
					ID: "support-id", ActorID: 11, ActorUsername: "admin", AppID: 42, AppSlug: "sales", ExpiresAt: time.Now().Add(15 * time.Minute)},
			}))
			if tc.policy != "" {
				resp.Header.Set("Content-Security-Policy", tc.policy)
			}
			if err := p.modifyResponseFor("sales")(resp); err != nil {
				t.Fatal(err)
			}
			if body := readBody(t, resp); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "Support session paused") {
				t.Fatalf("status=%d body=%s", resp.StatusCode, body)
			}
		})
	}
}

func TestSupportSessionNativeEndFormIgnoresConnectSrc(t *testing.T) {
	p := New()
	p.SetSupportSessions(true)
	resp := htmlResponse("sales", testShell)
	resp.Request = resp.Request.WithContext(auth.WithUser(resp.Request.Context(), &auth.ContextUser{
		ID: 22, Username: "alice", Role: "viewer", SupportSession: &auth.SupportSessionContext{
			ID: "support-id", ActorID: 11, ActorUsername: "admin", AppID: 42, AppSlug: "sales", ExpiresAt: time.Now().Add(15 * time.Minute)},
	}))
	resp.Header.Set("Content-Security-Policy", "script-src 'self'; connect-src 'none'; form-action 'self'")
	if err := p.modifyResponseFor("sales")(resp); err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `<form method="post"`) {
		t.Fatalf("status=%d; native stop form missing: %s", resp.StatusCode, body)
	}
}

func TestSupportSessionPreservesAndExtendsEveryCSPHeader(t *testing.T) {
	p := New()
	p.SetSupportSessions(true)
	resp := htmlResponse("sales", testShell)
	resp.Request = resp.Request.WithContext(auth.WithUser(resp.Request.Context(), &auth.ContextUser{
		ID: 22, Username: "alice", Role: "viewer", SupportSession: &auth.SupportSessionContext{
			ID: "support-id", ActorID: 11, ActorUsername: "admin", AppID: 42, AppSlug: "sales", ExpiresAt: time.Now().Add(15 * time.Minute)},
	}))
	resp.Header["Content-Security-Policy"] = []string{"default-src 'self'", "script-src 'self'"}
	if err := p.modifyResponseFor("sales")(resp); err != nil {
		t.Fatal(err)
	}
	values := resp.Header.Values("Content-Security-Policy")
	if len(values) != 2 {
		t.Fatalf("CSP values collapsed: %#v", values)
	}
	for _, value := range values {
		if !strings.Contains(value, supportui.CSPHash) {
			t.Fatalf("CSP value was not independently extended: %q", value)
		}
	}
}

func TestInjectAppFavicon_PreservesAuthoredIdentity(t *testing.T) {
	const authored = `<!doctype html><html><head><link rel="shortcut icon" href="/app-owned.ico"></head><body>App</body></html>`
	resp := htmlResponse("demo", authored)
	if err := injectPageHTML(func(*http.Request) []pageScript { return nil }, func() string { return favicon.AppURL("demo") }, nil)(resp); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if got := readBody(t, resp); got != authored {
		t.Fatalf("app-authored favicon was altered: %s", got)
	}
}

func TestInjectAppFavicon_AddsContextualIdentity(t *testing.T) {
	resp := htmlResponse("demo", testShell)
	resp.Header.Set("ETag", `"app-shell"`)
	resp.Header.Set("Content-Security-Policy", "default-src 'self'")
	if err := injectPageHTML(func(*http.Request) []pageScript { return nil }, func() string { return favicon.AppURL("demo") }, nil)(resp); err != nil {
		t.Fatalf("inject: %v", err)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, favicon.Link(favicon.AppURL("demo"))) {
		t.Fatalf("contextual favicon missing: %s", body)
	}
	if resp.Header.Get("ETag") != "" {
		t.Fatal("ETag survived the favicon body rewrite")
	}
}

func TestInjectAppFavicon_RespectsImageCSP(t *testing.T) {
	resp := htmlResponse("demo", testShell)
	resp.Header.Set("Content-Security-Policy", "default-src 'none'; img-src data:")
	if err := injectPageHTML(func(*http.Request) []pageScript { return nil }, func() string { return favicon.AppURL("demo") }, nil)(resp); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if got := readBody(t, resp); got != testShell {
		t.Fatalf("favicon was injected through a CSP that blocks same-origin images: %s", got)
	}
}

func TestInjectAppTitle_PreservesAuthoredTitle(t *testing.T) {
	resp := htmlResponse("demo", testShell)
	if err := injectPageHTML(func(*http.Request) []pageScript { return nil }, nil, func() string { return "Revenue Forecast · ShinyHub" })(resp); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if got := readBody(t, resp); got != testShell {
		t.Fatalf("app-authored title was altered: %s", got)
	}
}

func TestInjectAppTitle_AddsFallbackWhenMissing(t *testing.T) {
	const untitled = `<!doctype html><html><head></head><body>App</body></html>`
	resp := htmlResponse("demo", untitled)
	resp.Header.Set("ETag", `"untitled-shell"`)
	if err := injectPageHTML(func(*http.Request) []pageScript { return nil }, nil, func() string { return "Revenue & Forecast · ShinyHub" })(resp); err != nil {
		t.Fatalf("inject: %v", err)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, `<title>Revenue &amp; Forecast · ShinyHub</title>`) {
		t.Fatalf("fallback title missing: %s", body)
	}
	if resp.Header.Get("ETag") != "" {
		t.Fatal("ETag survived the title body rewrite")
	}
}

func TestCSPAllowsSelfImage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy string
		want   bool
	}{
		{name: "no policy", want: true},
		{name: "self in default", policy: "default-src 'self'", want: true},
		{name: "img overrides denied default", policy: "default-src 'none'; img-src 'self'", want: true},
		{name: "img overrides allowed default", policy: "default-src 'self'; img-src data:", want: false},
		{name: "denied default", policy: "default-src 'none'", want: false},
		{name: "unrelated directive", policy: "script-src 'none'", want: true},
		{name: "wildcard images", policy: "img-src *", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cspAllowsSelfImage(tc.policy); got != tc.want {
				t.Fatalf("cspAllowsSelfImage(%q) = %v, want %v", tc.policy, got, tc.want)
			}
		})
	}
}

func TestInjectStatusOverlay_LeavesUninjectableResponsesByteIdentical(t *testing.T) {
	const payload = `{"ok":true}`
	cases := map[string]func(*http.Response){
		"json": func(r *http.Response) { r.Header.Set("Content-Type", "application/json") },
		"gzip": func(r *http.Response) { r.Header.Set("Content-Encoding", "gzip") },
		"404":  func(r *http.Response) { r.StatusCode = http.StatusNotFound },
		"html without a closing body tag": func(r *http.Response) {
			r.Body = io.NopCloser(strings.NewReader(payload))
		},
		"app forbids scripts": func(r *http.Response) {
			r.Header.Set("Content-Security-Policy", "script-src 'none'")
			r.Body = io.NopCloser(strings.NewReader(payload))
		},
		"app forbids scripts via report-only": func(r *http.Response) {
			r.Header.Set("Content-Security-Policy-Report-Only", "default-src 'none'")
			r.Body = io.NopCloser(strings.NewReader(payload))
		},
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			resp := htmlResponse("demo", payload)
			resp.Header.Set("ETag", `"abc123"`)
			mut(resp)
			before := resp.Header.Clone()

			if err := injectPageScripts(overlayOnly("demo"))(resp); err != nil {
				t.Fatalf("inject: %v", err)
			}
			if got := readBody(t, resp); got != payload {
				t.Fatalf("body was altered: %q", got)
			}
			if resp.Header.Get("ETag") != before.Get("ETag") {
				t.Fatal("ETag was dropped without a rewrite")
			}
			if resp.Header.Get("Content-Security-Policy") != before.Get("Content-Security-Policy") {
				t.Fatalf("CSP changed without a rewrite: %q", resp.Header.Get("Content-Security-Policy"))
			}
		})
	}
}

func TestInjectStatusOverlay_OversizeBodyPassesThroughWhole(t *testing.T) {
	// The cap exists so one enormous page cannot be held in memory. It must not
	// also truncate that page: the bytes already read to discover the size have
	// to be spliced back in front of the rest.
	huge := strings.Repeat("x", overlayMaxBodyBytes+4096) + "</body>"
	resp := htmlResponse("demo", huge)
	resp.Header.Set("ETag", `"abc123"`)

	if err := injectPageScripts(overlayOnly("demo"))(resp); err != nil {
		t.Fatalf("inject: %v", err)
	}
	got := readBody(t, resp)
	if got != huge {
		t.Fatalf("oversize body was corrupted: got %d bytes, want %d", len(got), len(huge))
	}
	if resp.Header.Get("ETag") == "" {
		t.Fatal("ETag was dropped for a body that was never rewritten")
	}
}

func TestInjectStatusOverlay_UnsetContentLengthStaysUnset(t *testing.T) {
	// A chunked response carries no Content-Length. Adding one here would
	// contradict Transfer-Encoding and can truncate the response at the client.
	resp := htmlResponse("demo", testShell)
	resp.Header.Del("Content-Length")
	resp.ContentLength = -1

	if err := injectPageScripts(overlayOnly("demo"))(resp); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if got := resp.Header.Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length %q was added to a chunked response", got)
	}
	if !strings.Contains(readBody(t, resp), "shinyhub-status-overlay-loader") {
		t.Fatal("chunked response was not injected into")
	}
}

func TestModifyResponseFor_FlagIsReadPerResponse(t *testing.T) {
	// Backends are registered once and live for the process. A flag captured at
	// registration would leave every already-registered app permanently on the
	// value it had at boot.
	p := New()
	hook := p.modifyResponseFor("demo")

	off := htmlResponse("demo", testShell)
	if err := hook(off); err != nil {
		t.Fatalf("hook: %v", err)
	}
	if strings.Contains(readBody(t, off), "shinyhub-status-overlay-loader") {
		t.Fatal("overlay injected while disabled; it must be opt-in")
	}

	p.SetStatusOverlay(true)
	on := htmlResponse("demo", testShell)
	if err := hook(on); err != nil {
		t.Fatalf("hook: %v", err)
	}
	if !strings.Contains(readBody(t, on), "shinyhub-status-overlay-loader") {
		t.Fatal("enabling the overlay did not affect an already-registered backend")
	}

	p.SetStatusOverlay(false)
	again := htmlResponse("demo", testShell)
	if err := hook(again); err != nil {
		t.Fatalf("hook: %v", err)
	}
	if strings.Contains(readBody(t, again), "shinyhub-status-overlay-loader") {
		t.Fatal("disabling the overlay did not take effect")
	}
}

func TestModifyResponseFor_StillFiltersReservedCookies(t *testing.T) {
	// The cookie filter is a security control and the overlay is a feature.
	// Chaining them must not make the control conditional on the feature.
	for _, enabled := range []bool{false, true} {
		p := New()
		p.SetStatusOverlay(enabled)
		resp := htmlResponse("demo", testShell)
		resp.Header.Add("Set-Cookie", auth.SessionCookieName+"=forged; Path=/")
		resp.Header.Add("Set-Cookie", "app_pref=dark; Path=/")

		if err := p.modifyResponseFor("demo")(resp); err != nil {
			t.Fatalf("overlay=%v: hook: %v", enabled, err)
		}
		got := resp.Header.Values("Set-Cookie")
		if len(got) != 1 || !strings.HasPrefix(got[0], "app_pref=") {
			t.Fatalf("overlay=%v: reserved cookie not filtered, got %v", enabled, got)
		}
	}
}

// gzipped returns s compressed, the way a real app backend answers a request
// that advertised gzip.
func gzipped(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// TestStatusOverlay_EndToEndThroughTheProxy drives a real backend through the
// real reverse proxy, because the request half and the response half of this
// feature only work as a pair and each passes its own unit tests alone.
//
// The case that makes it worth the setup: a backend that compresses. Every
// serious Shiny/FastAPI deployment does, so if the Director did not clear
// Accept-Encoding the response would arrive gzipped, injectableResponse would
// decline it, and the overlay would be dead in production while every unit test
// stayed green.
func TestStatusOverlay_EndToEndThroughTheProxy(t *testing.T) {
	const css = "body{color:red}"
	var seen sync.Map // path -> Accept-Encoding the backend received

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ae := r.Header.Get("Accept-Encoding")
		seen.Store(r.URL.Path, ae)
		wantsGzip := strings.Contains(ae, "gzip")
		if r.URL.Path == "/style.css" {
			w.Header().Set("Content-Type", "text/css")
			if wantsGzip {
				w.Header().Set("Content-Encoding", "gzip")
				w.Write(gzipped(t, css))
				return
			}
			w.Write([]byte(css))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if wantsGzip {
			w.Header().Set("Content-Encoding", "gzip")
			w.Write(gzipped(t, testShell))
			return
		}
		w.Write([]byte(testShell))
	}))
	defer backend.Close()

	serve := func(p *Proxy, target, dest string) *http.Response {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Sec-Fetch-Dest", dest)
		req.Header.Set("Accept-Encoding", "gzip, br")
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		return rec.Result()
	}

	newProxy := func(overlay bool) *Proxy {
		t.Helper()
		p := New()
		p.SetStatusOverlay(overlay)
		p.SetPoolSize("demo", 1)
		if err := p.RegisterReplica("demo", 0, backend.URL, nil, 0); err != nil {
			t.Fatalf("register: %v", err)
		}
		return p
	}

	t.Run("a compressing backend still gets the overlay", func(t *testing.T) {
		resp := serve(newProxy(true), "/app/demo/", "document")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
		ae, _ := seen.Load("/")
		if ae != "gzip" {
			t.Fatalf("backend saw Accept-Encoding %q; the hop to the app must stay compressed", ae)
		}
		if got := resp.Header.Get("Content-Encoding"); got != "" {
			t.Fatalf("client got Content-Encoding %q for a body we rewrote", got)
		}
		body := readBody(t, resp)
		if !strings.Contains(body, "shinyhub-status-overlay-loader") {
			t.Fatalf("overlay missing from a proxied page load:\n%s", body)
		}
		if !strings.Contains(body, `id="app-root"`) {
			t.Fatal("the app's own markup did not survive the rewrite")
		}
	})

	t.Run("sub-resources keep their compression and their bytes", func(t *testing.T) {
		resp := serve(newProxy(true), "/app/demo/style.css", "style")
		ae, _ := seen.Load("/style.css")
		if ae != "gzip, br" {
			t.Fatalf("sub-resource Accept-Encoding was rewritten to %q", ae)
		}
		if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding = %q, want gzip passed through", got)
		}
		got, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		zr, err := gzip.NewReader(bytes.NewReader(got))
		if err != nil {
			t.Fatalf("proxied css is not valid gzip: %v", err)
		}
		plain, err := io.ReadAll(zr)
		if err != nil {
			t.Fatalf("inflate: %v", err)
		}
		if string(plain) != css {
			t.Fatalf("css was altered: %q", plain)
		}
	})

	t.Run("disabled leaves the transfer exactly as it was", func(t *testing.T) {
		resp := serve(newProxy(false), "/app/demo/", "document")
		ae, _ := seen.Load("/")
		if ae != "gzip, br" {
			t.Fatalf("Accept-Encoding was touched with the overlay off: %q", ae)
		}
		if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding = %q, want the backend's gzip untouched", got)
		}
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if bytes.Contains(raw, []byte("shinyhub-status-overlay-loader")) {
			t.Fatal("overlay injected while disabled")
		}
	})
}

func TestRelaxEncodingForInjection(t *testing.T) {
	page := func() *http.Request {
		r := appPageLoad("demo")
		r.Header.Set("Accept-Encoding", "gzip, br")
		return r
	}
	sub := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/app/demo/style.css", nil)
		r.Header.Set("Sec-Fetch-Dest", "style")
		r.Header.Set("Accept-Encoding", "gzip, br")
		return r
	}

	t.Run("both injections off leaves every request alone", func(t *testing.T) {
		p := New()
		for _, r := range []*http.Request{page(), sub()} {
			p.relaxEncodingForInjection(r)
			if r.Header.Get("Accept-Encoding") != "gzip, br" {
				t.Fatalf("Accept-Encoding changed with nothing to inject: %q", r.Header.Get("Accept-Encoding"))
			}
		}
	})

	// Each injection is tested alone, because the defect this guards against is
	// exactly the one-sided condition: relaxing only for the overlay leaves the
	// switcher un-injectable on any deployment that turned the overlay off, and
	// the symptom is a missing switcher with no error anywhere.
	for _, tc := range []struct {
		name string
		on   func(*Proxy)
	}{
		{"status overlay alone", func(p *Proxy) { p.SetStatusOverlay(true) }},
		{"app switcher alone", func(p *Proxy) { p.SetAppNav(true, "https://hub.example.com/") }},
		{"app favicon alone", func(p *Proxy) { p.SetAppFavicon(true) }},
	} {
		t.Run(tc.name+" clears it for page loads only", func(t *testing.T) {
			p := New()
			tc.on(p)

			r := page()
			p.relaxEncodingForInjection(r)
			if got := r.Header.Get("Accept-Encoding"); got != "" {
				t.Fatalf("page load kept Accept-Encoding %q; the body would arrive compressed", got)
			}

			s := sub()
			p.relaxEncodingForInjection(s)
			if got := s.Header.Get("Accept-Encoding"); got != "gzip, br" {
				t.Fatalf("sub-resource lost its Accept-Encoding %q; it is never injected into", got)
			}
		})
	}
}

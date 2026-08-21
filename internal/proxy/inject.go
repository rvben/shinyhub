package proxy

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/rvben/shinyhub/internal/appnav"
)

// overlayScript is the status overlay injected into app page loads. See
// assets/overlay.js for what it does and why it is safe to run inside an app
// this server did not write.
//
//go:embed assets/overlay.js
var overlayScript string

// overlayCSPHash is the CSP source expression allowing overlayScript as an
// inline block. It is computed from the embedded bytes rather than written by
// hand, so the hash cannot drift from the script that is actually served.
//
// The script body is identical for every app (all per-app values ride on the
// tag's data-* attributes, which a hash does not cover), so this single source
// admits the overlay across the whole fleet.
var overlayCSPHash = "'sha256-" + base64.StdEncoding.EncodeToString(hashOf(overlayScript)) + "'"

func hashOf(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

const (
	// overlayMaxBodyBytes caps how much of a response is buffered looking for
	// the insertion point. App shells are a few tens of KB; anything past this
	// is streamed through untouched rather than held in memory.
	overlayMaxBodyBytes = 2 << 20

	// overlayPollMS and overlayMaxPolls give the client a ~60s budget, matching
	// the loading page's own give-up window so the two waits feel like one
	// product rather than two.
	overlayPollMS   = 3000
	overlayMaxPolls = 20
)

// overlaySnippet renders the <script> tag for slug. Only attributes vary per
// app; the body between the tags is always overlayScript verbatim, which is
// what keeps one CSP hash valid everywhere.
func overlaySnippet(slug string) string {
	readyURL := "/app/" + slug + readySuffix
	return `<script id="shinyhub-status-overlay-loader"` +
		` data-ready-url="` + html.EscapeString(readyURL) + `"` +
		` data-poll-ms="` + strconv.Itoa(overlayPollMS) + `"` +
		` data-max-polls="` + strconv.Itoa(overlayMaxPolls) + `">` +
		overlayScript +
		`</script>`
}

// pageScript is one script ShinyHub adds to an app's HTML: the tag to splice in
// and the CSP source expression that admits it. Pairing them in one value is
// what keeps a script from being injected without its hash, which would leave a
// CSP-enforcing app with a blocked script and a console error.
type pageScript struct {
	snippet string
	cspHash string
}

// overlayPageScript is the status overlay as an injectable script.
func overlayPageScript(slug string) pageScript {
	return pageScript{snippet: overlaySnippet(slug), cspHash: overlayCSPHash}
}

// navPageScript is the app switcher as an injectable script.
func navPageScript(slug, homeURL string) pageScript {
	return pageScript{snippet: appnav.Snippet(slug, homeURL), cspHash: appnav.CSPHash}
}

// extendCSPForScripts returns policy with each hash allowed for scripts, and
// reports whether injection may proceed at all.
//
// The rule this function exists to hold: ShinyHub narrows an app's declared
// policy by exactly the known scripts it is itself adding, or it does not
// inject. It never widens one, never adds 'unsafe-inline', and never guesses at
// a policy it cannot parse. TestSecurityHeaders_ProxiedAppsUntouched encodes the
// surrounding principle that app security headers are the app's to set; adding a
// hash for a script this server is itself adding is the narrowest possible
// exception, and refusing to inject is always the safe alternative.
func extendCSPForScripts(policy string, hashes []string) (string, bool) {
	if strings.TrimSpace(policy) == "" || len(hashes) == 0 {
		return policy, true
	}
	parts := strings.Split(policy, ";")
	scriptIdx, defaultIdx := -1, -1
	for i, part := range parts {
		name, _, _ := strings.Cut(strings.TrimSpace(part), " ")
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "script-src":
			scriptIdx = i
		case "default-src":
			defaultIdx = i
		}
	}

	switch {
	case scriptIdx >= 0:
		val := parts[scriptIdx]
		if hasNoneSource(val) {
			// 'none' means the app forbids every script. It is also invalid
			// beside any other source, so a hash cannot be added without
			// rewriting the author's intent. Decline instead.
			return policy, false
		}
		out := strings.TrimRight(val, " ")
		for _, h := range hashes {
			// Already-present hashes are skipped rather than repeated, so a
			// response that somehow passes through twice does not accumulate
			// duplicate sources.
			if !strings.Contains(out, h) {
				out += " " + h
			}
		}
		if out == val {
			return policy, true
		}
		parts[scriptIdx] = out
		return strings.Join(parts, ";"), true

	case defaultIdx >= 0:
		// No script-src, so scripts fall back to default-src. Adding a
		// script-src that copies default-src's sources plus the hashes leaves
		// every other resource type governed exactly as before.
		val := strings.TrimSpace(parts[defaultIdx])
		if hasNoneSource(val) {
			return policy, false
		}
		_, sources, _ := strings.Cut(val, " ")
		sources = strings.TrimSpace(sources)
		if sources == "" {
			return policy, false
		}
		return strings.TrimRight(policy, "; ") + "; script-src " + sources + " " + strings.Join(hashes, " "), true

	default:
		// Neither directive: scripts are unrestricted by this policy already.
		return policy, true
	}
}

func hasNoneSource(directive string) bool {
	for _, f := range strings.Fields(directive) {
		if strings.EqualFold(f, "'none'") {
			return true
		}
	}
	return false
}

// relaxEncodingForInjection clears Accept-Encoding on an outbound page load so
// the body arrives in a form a script can be spliced into.
//
// Deleting the header is deliberate rather than forcing "identity": with no
// Accept-Encoding of its own, Go's Transport asks for gzip itself and decodes
// the result transparently, dropping Content-Encoding and Content-Length. So
// the hop to the backend stays compressed exactly as before and only the
// in-process view is plain. Forcing identity would instead move a real,
// uncompressed page shell across that hop on every navigation.
//
// It is confined to page loads: sub-resources and WebSocket upgrades are never
// injected into, and re-encoding their traffic to reach that conclusion would
// be pure cost. Compression toward the visitor is unaffected for those, and for
// the shell itself the edge (Caddy in the reference deployment) still
// compresses on the way out.
//
// It relaxes when EITHER injection is on, because either one alone needs a
// readable body. Tying it to the overlay alone would leave the switcher silently
// un-injectable whenever an operator turned the overlay off.
func (p *Proxy) relaxEncodingForInjection(req *http.Request) {
	if !p.injectsScripts() || !isPageLoad(req) {
		return
	}
	req.Header.Del("Accept-Encoding")
}

// injectsScripts reports whether any injection is currently enabled.
func (p *Proxy) injectsScripts() bool {
	return p.statusOverlay.Load() || p.appNav.Load() != nil
}

// withAppNav returns one of ShinyHub's own pages with the app switcher spliced
// in, or the page unchanged when the switcher is off or the page has no </body>.
//
// This is the deliberately simpler sibling of injectPageScripts. These pages are
// ShinyHub's, not the app's: there is no CSP of someone else's to narrow, no
// compressed body to reason about, and no risk of breaking markup this server
// did not write. It matters most precisely here, on the surfaces a visitor lands
// on when the app they asked for is not available: without it, "this app is
// stopped" is the end of the road rather than a place to pick a different app.
//
// The status overlay is deliberately not added to these pages. They carry their
// own reload logic, which is the same job.
func (p *Proxy) withAppNav(page, slug string) string {
	nav := p.appNav.Load()
	if nav == nil {
		return page
	}
	out, ok := appnav.SpliceIntoBody([]byte(page), appnav.Snippet(slug, nav.homeURL))
	if !ok {
		return page
	}
	return string(out)
}

// pageScriptsFor returns the scripts to splice into slug's HTML right now.
//
// The toggles are read per response rather than captured when a backend is
// registered, because backends are registered once and live for the process: a
// value sampled at registration would make SetStatusOverlay and SetAppNav
// silently apply only to apps deployed after the call.
func (p *Proxy) pageScriptsFor(slug string) []pageScript {
	var scripts []pageScript
	if p.statusOverlay.Load() {
		scripts = append(scripts, overlayPageScript(slug))
	}
	if nav := p.appNav.Load(); nav != nil {
		scripts = append(scripts, navPageScript(slug, nav.homeURL))
	}
	return scripts
}

// injectableResponse reports whether resp is a top-level HTML page load this
// proxy may rewrite.
//
// Every condition is a reason to decline, and declining costs nothing but the
// overlay: a mangled response would cost the app. In particular a body that
// arrived compressed is passed through rather than decompressed, because the
// Director drops Accept-Encoding on page loads precisely so this case is the
// rare one (a backend that compresses unasked), and handling it would mean
// re-encoding a body to match a header the backend chose.
func injectableResponse(resp *http.Response) bool {
	if resp == nil || resp.Body == nil || resp.Request == nil {
		return false
	}
	if resp.StatusCode != http.StatusOK {
		return false
	}
	if !isPageLoad(resp.Request) {
		return false
	}
	if enc := strings.TrimSpace(resp.Header.Get("Content-Encoding")); enc != "" && !strings.EqualFold(enc, "identity") {
		return false
	}
	ct, _, _ := strings.Cut(resp.Header.Get("Content-Type"), ";")
	return strings.EqualFold(strings.TrimSpace(ct), "text/html")
}

// injectPageScripts is the ModifyResponse hook that adds ShinyHub's own scripts
// to an app's HTML. Any condition it cannot satisfy leaves the response
// byte-for-byte unchanged.
//
// scripts is a function, not a slice, so the set is resolved per response and an
// operator's toggle reaches backends that were registered before it. All of them
// are spliced in one pass: a second pass would mean buffering and copying the
// whole page again for no gain.
func injectPageScripts(scripts func() []pageScript) func(*http.Response) error {
	return func(resp *http.Response) error {
		if !injectableResponse(resp) {
			return nil
		}
		wanted := scripts()
		if len(wanted) == 0 {
			return nil
		}

		// Read one byte past the cap so hitting it is distinguishable from a
		// body that merely ends there.
		orig := resp.Body
		buf, err := io.ReadAll(io.LimitReader(orig, overlayMaxBodyBytes+1))
		if err != nil || len(buf) > overlayMaxBodyBytes {
			// Either the read failed partway or the body is larger than we are
			// willing to hold. In both cases the bytes already consumed cannot
			// be un-read, so splice them back in front of the remainder and let
			// the normal copy carry on (and surface any error) untouched.
			resp.Body = struct {
				io.Reader
				io.Closer
			}{io.MultiReader(bytes.NewReader(buf), orig), orig}
			return nil
		}
		_ = orig.Close()

		restore := func() {
			resp.Body = io.NopCloser(bytes.NewReader(buf))
		}

		hashes := make([]string, 0, len(wanted))
		var snippets strings.Builder
		for _, s := range wanted {
			hashes = append(hashes, s.cspHash)
			snippets.WriteString(s.snippet)
		}

		policy, ok := extendCSPForScripts(resp.Header.Get("Content-Security-Policy"), hashes)
		if !ok {
			restore()
			return nil
		}
		reportPolicy, ok := extendCSPForScripts(resp.Header.Get("Content-Security-Policy-Report-Only"), hashes)
		if !ok {
			restore()
			return nil
		}

		out, injected := appnav.SpliceIntoBody(buf, snippets.String())
		if !injected {
			restore()
			return nil
		}

		if policy != "" {
			resp.Header.Set("Content-Security-Policy", policy)
		}
		if reportPolicy != "" {
			resp.Header.Set("Content-Security-Policy-Report-Only", reportPolicy)
		}
		// The body no longer matches whatever the backend hashed it to.
		resp.Header.Del("ETag")
		resp.Body = io.NopCloser(bytes.NewReader(out))
		if resp.Header.Get("Content-Length") != "" {
			resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
		}
		resp.ContentLength = int64(len(out))
		return nil
	}
}

// chainModifyResponse runs each hook in order, stopping at the first error.
func chainModifyResponse(hooks ...func(*http.Response) error) func(*http.Response) error {
	return func(resp *http.Response) error {
		for _, h := range hooks {
			if h == nil {
				continue
			}
			if err := h(resp); err != nil {
				return err
			}
		}
		return nil
	}
}

// modifyResponseFor builds the ModifyResponse chain for one backend. The
// Set-Cookie filter is unconditional; both injections are opt-in, wired by
// main.go from config so tests and embedders are not implicitly rewriting app
// HTML. See pageScriptsFor for why the toggles are read per response.
func (p *Proxy) modifyResponseFor(slug string) func(*http.Response) error {
	return chainModifyResponse(filterReservedSetCookies, injectPageScripts(func() []pageScript {
		return p.pageScriptsFor(slug)
	}))
}

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
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/favicon"
	"github.com/rvben/shinyhub/internal/supportui"
	xhtml "golang.org/x/net/html"
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
	snippet  string
	render   func() string // optional markup is materialized only after admission
	cspHash  string
	required bool
	fallback string
}

// overlayPageScript is the status overlay as an injectable script.
func overlayPageScript(slug string) pageScript {
	return pageScript{render: func() string { return overlaySnippet(slug) }, cspHash: overlayCSPHash}
}

// navPageScript is the app switcher as an injectable script.
func navPageScript(slug, name, homeURL, generation string) pageScript {
	return pageScript{render: func() string { return appnav.SnippetWithGeneration(slug, name, homeURL, generation) }, cspHash: appnav.CSPHash}
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
	// A comma separates independently enforced policies when multiple header
	// values have been serialized into one field. Treating the suffix as a
	// source expression could accidentally ignore a stricter second policy.
	if strings.Contains(policy, ",") {
		return policy, false
	}
	parts := strings.Split(policy, ";")
	scriptElemIdx, scriptIdx, defaultIdx := -1, -1, -1
	for i, part := range parts {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		switch strings.ToLower(fields[0]) {
		case "script-src-elem":
			if scriptElemIdx >= 0 {
				return policy, false
			}
			scriptElemIdx = i
		case "script-src":
			if scriptIdx >= 0 {
				return policy, false
			}
			scriptIdx = i
		case "default-src":
			if defaultIdx >= 0 {
				return policy, false
			}
			defaultIdx = i
		}
	}

	switch {
	case scriptElemIdx >= 0:
		val := parts[scriptElemIdx]
		if hasNoneSource(val) {
			return policy, false
		}
		out := appendCSPHashes(val, hashes)
		parts[scriptElemIdx] = out
		return strings.Join(parts, ";"), true
	case scriptIdx >= 0:
		val := parts[scriptIdx]
		if hasNoneSource(val) {
			// 'none' means the app forbids every script. It is also invalid
			// beside any other source, so a hash cannot be added without
			// rewriting the author's intent. Decline instead.
			return policy, false
		}
		out := appendCSPHashes(val, hashes)
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

// cspAllowsSupportForm reports whether the mandatory same-origin native stop
// form remains usable. form-action has no default-src fallback; sandbox does
// require allow-forms. Unknown explicit targets fail closed.
func cspAllowsSupportForm(policy string) bool {
	if strings.TrimSpace(policy) == "" {
		return true
	}
	// Commas delimit policy lists, not directives. Fail closed instead of
	// accidentally folding a stricter second policy into the first directive.
	if strings.Contains(policy, ",") {
		return false
	}
	formSeen, sandboxSeen := false, false
	for _, part := range strings.Split(policy, ";") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) == 0 {
			continue
		}
		switch strings.ToLower(fields[0]) {
		case "form-action":
			if formSeen {
				return false
			}
			formSeen = true
			allowed := false
			for _, source := range fields[1:] {
				if source == "*" || strings.EqualFold(source, "'self'") {
					allowed = true
				}
			}
			if !allowed {
				return false
			}
		case "sandbox":
			if sandboxSeen {
				return false
			}
			sandboxSeen = true
			allowForms, allowSameOrigin := false, false
			for _, token := range fields[1:] {
				if strings.EqualFold(token, "allow-forms") {
					allowForms = true
				}
				if strings.EqualFold(token, "allow-same-origin") {
					allowSameOrigin = true
				}
			}
			if !allowForms || !allowSameOrigin {
				return false
			}
		}
	}
	return true
}

func cspHeadersAllowSupportForm(header http.Header) bool {
	values := header.Values("Content-Security-Policy")
	for _, policy := range values {
		if !cspAllowsSupportForm(policy) {
			return false
		}
	}
	return true
}

func appendCSPHashes(directive string, hashes []string) string {
	out := strings.TrimRight(directive, " ")
	for _, h := range hashes {
		if !strings.Contains(out, h) {
			out += " " + h
		}
	}
	return out
}

func extendedCSPHeaderValues(header http.Header, name string, hashes []string) ([]string, bool) {
	key := http.CanonicalHeaderKey(name)
	values := header[key]
	if len(values) == 0 {
		return nil, true
	}
	updated := make([]string, len(values))
	for i, value := range values {
		policy, ok := extendCSPForScripts(value, hashes)
		if !ok {
			return nil, false
		}
		updated[i] = policy
	}
	return updated, true
}

// Attribute names are ASCII and are not entity-decoded by the HTML parser.
// If no http-equiv spelling exists, a meta policy is impossible. Candidates
// still use the full parser below, preserving foreign-content semantics.
func containsHTTPEquiv(body []byte) bool {
	for start := 0; start < len(body); {
		i := bytes.IndexByte(body[start:], '-')
		if i < 0 {
			return false
		}
		i += start
		if i >= 4 && i+6 <= len(body) && bytes.EqualFold(body[i-4:i+6], []byte("http-equiv")) {
			return true
		}
		start = i + 1
	}
	return false
}

func containsMetaCSP(body []byte) bool {
	// Without a meta start tag there can be no meta policy. Keep the full HTML
	// parser for candidates so its context handling (including foreign content
	// and malformed markup) remains the authority for this security decision.
	if lastHTMLTag(body, "<meta") < 0 || !containsHTTPEquiv(body) {
		return false
	}
	doc, err := xhtml.Parse(bytes.NewReader(body))
	if err != nil {
		return true
	}
	var walk func(*xhtml.Node) bool
	walk = func(n *xhtml.Node) bool {
		if n.Type == xhtml.ElementNode && strings.EqualFold(n.Data, "meta") {
			for _, attr := range n.Attr {
				if strings.EqualFold(attr.Key, "http-equiv") && strings.EqualFold(strings.TrimSpace(attr.Val), "content-security-policy") {
					return true
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if walk(child) {
				return true
			}
		}
		return false
	}
	return walk(doc)
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
// It relaxes whenever any page enhancement is on, because each needs a readable
// body. Tying it to the overlay alone would leave the switcher or favicon
// silently uninjectable whenever an operator turned the overlay off.
func (p *Proxy) relaxEncodingForInjection(req *http.Request) {
	if !p.injectsPageHTML() || !isPageLoad(req) {
		return
	}
	req.Header.Del("Accept-Encoding")
}

// injectsPageHTML reports whether any page-level enhancement is enabled.
func (p *Proxy) injectsPageHTML() bool {
	return p.statusOverlay.Load() || p.appNav.Load() != nil || p.appFavicon.Load() || p.supportSessions.Load()
}

// decorateAppPage gives one of ShinyHub's own app pages its contextual favicon
// and, when enabled, splices in the app switcher.
//
// This is the deliberately simpler sibling of injectPageHTML. These pages are
// ShinyHub's, not the app's: there is no CSP of someone else's to narrow, no
// compressed body to reason about, and no risk of breaking markup this server
// did not write. It matters most precisely here, on the surfaces a visitor lands
// on when the app they asked for is not available: without it, "this app is
// stopped" is the end of the road rather than a place to pick a different app.
//
// The status overlay is deliberately not added to these pages. They carry their
// own reload logic, which is the same job.
func (p *Proxy) decorateAppPage(page, slug string, r *http.Request) string {
	out := []byte(page)
	if p.appFavicon.Load() {
		out, _ = favicon.Ensure(out, favicon.AppURL(slug))
		out, _ = favicon.SetTitle(out, p.appPageTitle(slug))
	}
	var snippets strings.Builder
	support := p.supportPageScript(r, slug)
	if support != nil {
		snippets.WriteString(support.snippet)
	}
	// A support session is deliberately confined to one app. Do not render the
	// fleet switcher while it is active: the root guard would reject the
	// resulting cross-app navigation anyway, and hiding that dead end makes the
	// boundary visible before the administrator clicks it.
	if nav := p.appNav.Load(); nav != nil && support == nil {
		snippets.WriteString(appnav.SnippetWithName(slug, p.appName(slug), nav.homeURL))
	}
	if snippets.Len() == 0 {
		return string(out)
	}
	out, ok := appnav.SpliceIntoBody(out, snippets.String())
	if !ok {
		return string(out)
	}
	return string(out)
}

// pageScriptsFor returns the scripts to splice into slug's HTML right now.
//
// The toggles are read per response rather than captured when a backend is
// registered, because backends are registered once and live for the process: a
// value sampled at registration would make SetStatusOverlay and SetAppNav
// silently apply only to apps deployed after the call.
func (p *Proxy) pageScriptsFor(r *http.Request, slug string, deploymentID int64) []pageScript {
	if r == nil || !isPageLoad(r) {
		return nil
	}
	var scripts []pageScript
	support := p.supportPageScript(r, slug)
	if support != nil {
		scripts = append(scripts, *support)
	}
	if p.statusOverlay.Load() {
		scripts = append(scripts, overlayPageScript(slug))
	}
	if nav := p.appNav.Load(); nav != nil && support == nil {
		scripts = append(scripts, navPageScript(slug, p.appName(slug), nav.homeURL, p.generationActivationToken(slug, deploymentID)))
	}
	return scripts
}

func (p *Proxy) supportPageScript(r *http.Request, slug string) *pageScript {
	if !p.supportSessions.Load() || r == nil || !isPageLoad(r) {
		return nil
	}
	user := auth.UserFromContext(r.Context())
	if user == nil || user.SupportSession == nil || user.SupportSession.AppSlug != slug {
		return nil
	}
	support := user.SupportSession
	return &pageScript{
		snippet:  supportui.Snippet(slug, support.ActorUsername, user.Username, support.ID, support.ExpiresAt),
		cspHash:  supportui.CSPHash,
		required: true,
		fallback: supportui.BlockedPage(slug, support.ActorUsername, user.Username, support.ExpiresAt),
	}
}

// injectableResponse reports whether resp is a top-level HTML page load this
// proxy may rewrite.
//
// Every condition is a reason to decline, and declining costs only ShinyHub's
// optional page enhancements: a mangled response would cost the app. In
// particular a body that arrived compressed is passed through rather than
// decompressed, because the
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
func injectPageScripts(scripts func(*http.Request) []pageScript) func(*http.Response) error {
	return injectPageHTML(scripts, nil, nil)
}

// injectPageHTML adds ShinyHub's optional scripts and contextual browser
// identity in a single bounded buffering pass. An app-authored title or favicon
// always wins. Script CSP changes and favicon insertion are independent: an app
// that refuses ShinyHub's scripts can still receive its favicon when its image
// policy permits same-origin resources.
func injectPageHTML(scripts func(*http.Request) []pageScript, faviconHref, titleFallback func() string) func(*http.Response) error {
	return func(resp *http.Response) error {
		if resp == nil || resp.Request == nil {
			return nil
		}
		wanted := scripts(resp.Request)
		if !injectableResponse(resp) {
			replaceWithRequiredFallback(resp, wanted)
			return nil
		}
		href := ""
		if faviconHref != nil {
			href = faviconHref()
		}
		title := ""
		if titleFallback != nil {
			title = titleFallback()
		}
		if len(wanted) == 0 && href == "" && title == "" {
			return nil
		}

		// A declared oversized page cannot be decorated. Preserve required support
		// fallbacks, but do not consume optional pass-through bodies at all.
		if resp.ContentLength > overlayMaxBodyBytes {
			replaceWithRequiredFallback(resp, wanted)
			return nil
		}

		// Read one byte past the cap so hitting it is distinguishable from a
		// body that merely ends there.
		orig := resp.Body
		buf, err := readPageBody(orig, resp.ContentLength)
		if err != nil || len(buf) > overlayMaxBodyBytes {
			if replaceWithRequiredFallback(resp, wanted) {
				return nil
			}
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

		if !cspAllowsSelfImage(resp.Header.Get("Content-Security-Policy")) {
			href = ""
		}
		headMarkup := favicon.FallbackMarkup(buf, href, title)
		var bodyMarkup []string
		scriptsInjected := false

		if len(wanted) > 0 {
			hashes := make([]string, 0, len(wanted))
			requiresSupportForm := false
			for _, s := range wanted {
				hashes = append(hashes, s.cspHash)
				requiresSupportForm = requiresSupportForm || s.required
			}

			policies, policyOK := extendedCSPHeaderValues(resp.Header, "Content-Security-Policy", hashes)
			reportPolicies, reportOK := extendedCSPHeaderValues(resp.Header, "Content-Security-Policy-Report-Only", hashes)
			policyOK = policyOK && !containsMetaCSP(buf) && (!requiresSupportForm || cspHeadersAllowSupportForm(resp.Header))
			if policyOK && reportOK {
				if lastHTMLTag(buf, "</body>") >= 0 {
					for _, script := range wanted {
						markup := script.snippet
						if script.render != nil {
							markup = script.render()
						}
						bodyMarkup = append(bodyMarkup, markup)
					}
					scriptsInjected = true
					if policies != nil {
						resp.Header[http.CanonicalHeaderKey("Content-Security-Policy")] = policies
					}
					if reportPolicies != nil {
						resp.Header[http.CanonicalHeaderKey("Content-Security-Policy-Report-Only")] = reportPolicies
					}
				}
			}
			if !scriptsInjected {
				for _, script := range wanted {
					if script.required {
						resp.Body = io.NopCloser(bytes.NewReader(buf))
						replaceWithRequiredFallback(resp, wanted)
						return nil
					}
				}
			}
		}

		out, changed := splicePageMarkup(buf, headMarkup, bodyMarkup...)
		if !changed {
			restore()
			return nil
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

// readPageBody uses a trustworthy size only as an allocation hint. Reading one
// extra byte still detects incorrect lengths and enforces the injection cap.
func readPageBody(body io.Reader, length int64) ([]byte, error) {
	if length < 0 || length > overlayMaxBodyBytes {
		return io.ReadAll(io.LimitReader(body, overlayMaxBodyBytes+1))
	}
	buf := make([]byte, int(length)+1)
	n := 0
	for n < len(buf) {
		read, err := body.Read(buf[n:])
		n += read
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			return buf[:n], err
		}
	}

	rest, err := io.ReadAll(io.LimitReader(body, int64(overlayMaxBodyBytes+1-len(buf))))
	return append(buf, rest...), err
}

// lastHTMLTag preserves the existing case-insensitive insertion semantics but
// skips plain text in bulk rather than comparing at every byte of a large page.
func lastHTMLTag(page []byte, tag string) int {
	for end := len(page); end > 0; {
		i := bytes.LastIndexByte(page[:end], '<')
		if i < 0 {
			return -1
		}
		if len(page)-i >= len(tag) && bytes.EqualFold(page[i:i+len(tag)], []byte(tag)) {
			return i
		}
		end = i
	}
	return -1
}

// splicePageMarkup assembles both edits once, preserving source bytes and even
// the ordering of malformed pages whose closing body precedes closing head.
func splicePageMarkup(page []byte, head string, body ...string) ([]byte, bool) {
	headAt, bodyAt := -1, -1
	bodySize := 0
	for _, part := range body {
		bodySize += len(part)
	}
	if head != "" {
		headAt = lastHTMLTag(page, "</head>")
	}
	if bodySize > 0 {
		bodyAt = lastHTMLTag(page, "</body>")
	}
	if headAt < 0 {
		head = ""
	}
	if bodyAt < 0 {
		body = nil
		bodySize = 0
	}
	if head == "" && bodySize == 0 {
		return page, false
	}
	out := make([]byte, 0, len(page)+len(head)+bodySize)
	first, second := []string{head}, body
	if headAt < 0 || (bodyAt >= 0 && bodyAt < headAt) {
		headAt, bodyAt, first, second = bodyAt, headAt, second, first
	}
	out = append(out, page[:headAt]...)
	for _, part := range first {
		out = append(out, part...)
	}
	if bodyAt >= 0 {
		out = append(out, page[headAt:bodyAt]...)
		for _, part := range second {
			out = append(out, part...)
		}
		headAt = bodyAt
	}
	out = append(out, page[headAt:]...)
	return out, true
}

func replaceWithRequiredFallback(resp *http.Response, scripts []pageScript) bool {
	var fallback string
	for _, script := range scripts {
		if script.required && script.fallback != "" {
			fallback = script.fallback
			break
		}
	}
	if fallback == "" || resp == nil {
		return false
	}
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
	resp.StatusCode = http.StatusConflict
	resp.Status = strconv.Itoa(http.StatusConflict) + " " + http.StatusText(http.StatusConflict)
	resp.Header.Set("Content-Type", "text/html; charset=utf-8")
	resp.Header.Set("Content-Security-Policy", supportui.PageCSP)
	resp.Header.Del("Content-Security-Policy-Report-Only")
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("ETag")
	resp.Header.Set("Cache-Control", "no-store")
	resp.Body = io.NopCloser(strings.NewReader(fallback))
	resp.ContentLength = int64(len(fallback))
	resp.Header.Set("Content-Length", strconv.Itoa(len(fallback)))
	return true
}

// cspAllowsSelfImage conservatively reports whether a same-origin favicon is
// admitted by the effective img-src directive. Unknown explicit source forms
// decline injection rather than widening or rewriting an app's policy.
func cspAllowsSelfImage(policy string) bool {
	if strings.TrimSpace(policy) == "" {
		return true
	}
	var fallback []string
	for _, part := range strings.Split(policy, ";") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) == 0 {
			continue
		}
		switch strings.ToLower(fields[0]) {
		case "img-src":
			return hasSelfImageSource(fields[1:])
		case "default-src":
			fallback = fields[1:]
		}
	}
	if fallback != nil {
		return hasSelfImageSource(fallback)
	}
	return true
}

func hasSelfImageSource(sources []string) bool {
	for _, source := range sources {
		if source == "*" || strings.EqualFold(source, "'self'") {
			return true
		}
	}
	return false
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
// Set-Cookie filter is unconditional; page enhancements are opt-in, wired by
// main.go so tests and embedders are not implicitly rewriting app HTML. See
// pageScriptsFor for why the toggles are read per response.
func (p *Proxy) modifyResponseFor(slug string, deploymentIDs ...int64) func(*http.Response) error {
	var deploymentID int64
	if len(deploymentIDs) > 0 {
		deploymentID = deploymentIDs[0]
	}
	return chainModifyResponse(filterReservedSetCookies, injectPageHTML(
		func(r *http.Request) []pageScript { return p.pageScriptsFor(r, slug, deploymentID) },
		func() string {
			if !p.appFavicon.Load() {
				return ""
			}
			return favicon.AppURL(slug)
		},
		func() string {
			if !p.appFavicon.Load() {
				return ""
			}
			return p.appPageTitle(slug)
		},
	))
}

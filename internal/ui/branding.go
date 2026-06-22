package ui

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/rvben/shinyhub/internal/config"
)

// Public is the small, documented branding object exposed inline in the SPA
// shell and at GET /.shinyhub/branding.json. URLs are browser-ready.
type Public struct {
	SiteTitle    string              `json:"site_title,omitempty"`
	Logo         string              `json:"logo,omitempty"`
	Favicon      string              `json:"favicon,omitempty"`
	PrimaryColor string              `json:"primary_color,omitempty"`
	FooterLinks  []config.FooterLink `json:"footer_links,omitempty"`
}

func assetURL(ref string, resolved map[string]string) string {
	if ref == "" {
		return ""
	}
	// resolved is accepted to keep a stable signature/intent with the /branding/
	// allow-list but is intentionally not consulted here - basenames are derived
	// via path.Base and the allow-list is enforced at serve time (Task 5).
	low := strings.ToLower(ref)
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		return ref
	}
	return "/branding/" + path.Base(ref)
}

// PublicBranding builds the browser-ready object. resolved is the
// basename->path allow-list (nil when only URLs/scalars are used).
func PublicBranding(b config.BrandingConfig, resolved map[string]string) Public {
	return Public{
		SiteTitle:    b.SiteTitle,
		Logo:         assetURL(b.Logo, resolved),
		Favicon:      assetURL(b.Favicon, resolved),
		PrimaryColor: b.Theme.PrimaryColor,
		FooterLinks:  b.FooterLinks,
	}
}

var (
	titleRe = regexp.MustCompile(`(?s)<title>.*?</title>`)
	headRe  = regexp.MustCompile(`</head>`)
)

// BrandingAssetHandler serves ONLY the basenames in the allow-list. There is
// no path arithmetic on request input: the trailing segment is looked up in
// the map, so traversal, encoded segments and symlink tricks cannot escape.
func BrandingAssetHandler(allow map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/branding/")
		if name == "" || strings.Contains(name, "/") {
			http.NotFound(w, r)
			return
		}
		abs, ok := allow[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, abs)
	})
}

// brandingInlineScript returns the body (between the <script></script> tags) of
// the inline branding bootstrap RenderIndex emits. It is the exact byte sequence
// a strict CSP must allow by hash, so it and the CSP source MUST be derived from
// this one builder to stay in lockstep.
//
// HTML-safe JSON: json.Marshal escapes <, >, & to < etc., which neutralises
// a </script> breakout in any string field.
func brandingInlineScript(p Public) (string, error) {
	j, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("ui: marshal branding: %w", err)
	}
	return "window.__SHINYHUB_BRANDING__=" + string(j) + ";", nil
}

// brandingInlineStyle returns the body of the inline <style> RenderIndex emits to
// set the primary brand color, and whether one is emitted at all (only when a
// primary color is configured).
func brandingInlineStyle(p Public) (string, bool) {
	if p.PrimaryColor == "" {
		return "", false
	}
	return ":root{--brand-primary:" + html.EscapeString(p.PrimaryColor) + "}", true
}

// cspHashSource renders the CSP source-expression ('sha256-<base64>') for an
// inline block's exact body, the form a strict script-src/style-src lists to
// allow that block without 'unsafe-inline'.
func cspHashSource(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// CSPInlineSources returns the CSP source expressions for the inline <script>
// and <style> blocks RenderIndex emits for p, so a strict Content-Security-Policy
// can allow exactly those blocks instead of 'unsafe-inline'. The style slice is
// empty when no primary color is configured. Because it reuses the same builders
// RenderIndex uses, the hashes always match the served bytes.
func CSPInlineSources(p Public) (scriptSources, styleSources []string, err error) {
	script, err := brandingInlineScript(p)
	if err != nil {
		return nil, nil, err
	}
	scriptSources = []string{cspHashSource(script)}
	if style, ok := brandingInlineStyle(p); ok {
		styleSources = []string{cspHashSource(style)}
	}
	return scriptSources, styleSources, nil
}

// RenderIndex injects branding into the stock SPA shell. Callers MUST only
// invoke this when branding is active; the zero-branding path serves raw
// bytes elsewhere and is never routed here.
func RenderIndex(raw []byte, p Public) ([]byte, error) {
	script, err := brandingInlineScript(p)
	if err != nil {
		return nil, err
	}

	out := raw
	if p.SiteTitle != "" {
		out = titleRe.ReplaceAllLiteral(out, []byte("<title>"+html.EscapeString(p.SiteTitle)+"</title>"))
	}

	var head bytes.Buffer
	if p.Favicon != "" {
		fmt.Fprintf(&head, "<link rel=\"icon\" href=\"%s\">\n", html.EscapeString(p.Favicon))
	}
	if style, ok := brandingInlineStyle(p); ok {
		fmt.Fprintf(&head, "<style>%s</style>\n", style)
	}
	fmt.Fprintf(&head, "<script>%s</script>\n", script)

	out = headRe.ReplaceAllLiteral(out, append(head.Bytes(), []byte("</head>")...))
	return out, nil
}

// StampAuthenticated marks the SPA shell as already-authenticated so the
// dashboard chrome paints immediately, skipping the boot splash and never
// flashing the login form. Callers invoke this only when the request that
// fetched the shell is itself authenticated (e.g. behind forward auth). It
// swaps the default data-auth="loading" for data-auth="in"; if the marker is
// absent (already stamped, or the shell changed), it returns the input
// unchanged.
func StampAuthenticated(shell []byte) []byte {
	return bytes.Replace(shell, []byte(`data-auth="loading"`), []byte(`data-auth="in"`), 1)
}

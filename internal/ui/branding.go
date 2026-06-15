package ui

import (
	"bytes"
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

// RenderIndex injects branding into the stock SPA shell. Callers MUST only
// invoke this when branding is active; the zero-branding path serves raw
// bytes elsewhere and is never routed here.
func RenderIndex(raw []byte, p Public) ([]byte, error) {
	// HTML-safe JSON: json.Marshal already escapes <, >, & to < etc.,
	// which neutralises a </script> breakout in any string field.
	j, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("ui: marshal branding: %w", err)
	}

	out := raw
	if p.SiteTitle != "" {
		out = titleRe.ReplaceAllLiteral(out, []byte("<title>"+html.EscapeString(p.SiteTitle)+"</title>"))
	}

	var head bytes.Buffer
	if p.Favicon != "" {
		fmt.Fprintf(&head, "<link rel=\"icon\" href=\"%s\">\n", html.EscapeString(p.Favicon))
	}
	if p.PrimaryColor != "" {
		fmt.Fprintf(&head, "<style>:root{--brand-primary:%s}</style>\n", html.EscapeString(p.PrimaryColor))
	}
	fmt.Fprintf(&head, "<script>window.__SHINYHUB_BRANDING__=%s;</script>\n", j)

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

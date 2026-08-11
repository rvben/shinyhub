package ui_test

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/ui"
)

func TestPublicBrandingMapsResolvedURLs(t *testing.T) {
	b := config.BrandingConfig{
		SiteTitle: "Acme", Logo: "logo.svg",
		Theme:       config.ThemeConfig{PrimaryColor: "#0a7d8c"},
		FooterLinks: []config.FooterLink{{Label: "S", URL: "https://e.com"}},
	}
	p := ui.PublicBranding(b, map[string]string{"logo.svg": "/abs/logo.svg"})
	if p.SiteTitle != "Acme" || p.PrimaryColor != "#0a7d8c" {
		t.Fatalf("scalars wrong: %+v", p)
	}
	if p.Logo != "/branding/logo.svg" {
		t.Fatalf("local logo must map to /branding/<base>, got %q", p.Logo)
	}
	if len(p.FooterLinks) != 1 || p.FooterLinks[0].URL != "https://e.com" {
		t.Fatalf("footer links wrong: %+v", p.FooterLinks)
	}

	pURL := ui.PublicBranding(config.BrandingConfig{Logo: "https://cdn/x.svg"}, nil)
	if pURL.Logo != "https://cdn/x.svg" {
		t.Fatalf("URL logo must pass through, got %q", pURL.Logo)
	}

	pUpper := ui.PublicBranding(config.BrandingConfig{Logo: "HTTPS://cdn/x.svg"}, nil)
	if pUpper.Logo != "HTTPS://cdn/x.svg" {
		t.Fatalf("uppercase-scheme URL must pass through preserving case, got %q", pUpper.Logo)
	}
}

// TestImageSources covers the CSP allowance for remotely-hosted branding
// images. `logo`/`favicon` accept an absolute http(s) URL, but the control-plane
// CSP is `img-src 'self' data:`, so without this the browser blocks the
// operator's logo and the front door renders a broken lockup.
func TestImageSources(t *testing.T) {
	// Local assets are served same-origin from /branding/, already covered by
	// 'self' - no widening at all.
	local := ui.PublicBranding(config.BrandingConfig{Logo: "logo.svg", Favicon: "fav.ico"},
		map[string]string{"logo.svg": "/abs/logo.svg", "fav.ico": "/abs/fav.ico"})
	if got := ui.ImageSources(local); len(got) != 0 {
		t.Fatalf("local assets must not widen img-src, got %v", got)
	}

	// A remote logo contributes its origin, not the full URL: CDNs redirect, and
	// a CSP path source would then fail to match.
	remote := ui.PublicBranding(config.BrandingConfig{Logo: "https://cdn.example.com/a/logo.svg"}, nil)
	if got := ui.ImageSources(remote); len(got) != 1 || got[0] != "https://cdn.example.com" {
		t.Fatalf("remote logo must contribute its origin, got %v", got)
	}

	// Two assets on one host collapse to a single source.
	same := ui.PublicBranding(config.BrandingConfig{
		Logo:    "https://cdn.example.com/logo.svg",
		Favicon: "https://cdn.example.com/fav.ico",
	}, nil)
	if got := ui.ImageSources(same); len(got) != 1 || got[0] != "https://cdn.example.com" {
		t.Fatalf("shared origin must be listed once, got %v", got)
	}

	// Distinct hosts, an explicit port, and an uppercase scheme all normalise.
	multi := ui.PublicBranding(config.BrandingConfig{
		Logo:    "HTTPS://Cdn.Example.com:8443/logo.svg",
		Favicon: "http://assets.example.org/fav.ico",
	}, nil)
	got := ui.ImageSources(multi)
	if len(got) != 2 || got[0] != "https://cdn.example.com:8443" || got[1] != "http://assets.example.org" {
		t.Fatalf("origins must be lowercased, port-preserving and deduped, got %v", got)
	}
}

func TestRenderIndexInjectsHeadAndEscapesScript(t *testing.T) {
	raw := []byte("<html><head><title>ShinyHub</title></head><body>X</body></html>")
	b := config.BrandingConfig{SiteTitle: `Acme</script><script>alert(1)</script>`}
	out, err := ui.RenderIndex(raw, ui.PublicBranding(b, nil))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "<title>Acme") {
		t.Fatalf("title not injected: %s", s)
	}
	if strings.Contains(s, "<script>alert(1)</script>") {
		t.Fatalf("inline branding JSON not escaped against </script> breakout: %s", s)
	}
	if !strings.Contains(s, "window.__SHINYHUB_BRANDING__") {
		t.Fatalf("inline branding object missing: %s", s)
	}
	// json.Marshal escapes < to <; verify the six-character sequence
	// backslash+u003c appears in the output so </script> cannot break out of
	// the inline JSON block.
	if !strings.Contains(s, "\\u003c") {
		t.Fatalf("inline branding JSON must unicode-escape '<' as \\u003c, got: %s", s)
	}

	// $0-regression: ReplaceAllLiteral must not expand $0 back to the original
	// match, which would leave the old title in place.
	raw2 := []byte("<html><head><title>ShinyHub</title></head><body>X</body></html>")
	out2, err := ui.RenderIndex(raw2, ui.PublicBranding(config.BrandingConfig{SiteTitle: "App $0"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out2), "ShinyHub") {
		t.Fatalf("$0 in SiteTitle must not expand to original match; got: %s", string(out2))
	}
}

func TestBrandingAssetHandler(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "logo.svg")
	if err := os.WriteFile(p, []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := ui.BrandingAssetHandler(map[string]string{"logo.svg": p})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/branding/logo.svg", nil))
	if rec.Code != 200 || rec.Body.String() != "<svg/>" {
		t.Fatalf("served file wrong: code=%d body=%q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct == "" {
		t.Fatal("Content-Type must be set")
	}

	for _, bad := range []string{"/branding/missing.svg", "/branding/../etc/passwd", "/branding/%2e%2e/x", "/branding/", "/branding/%2e%2e", "/branding/."} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest("GET", bad, nil))
		if r.Code != 404 {
			t.Errorf("%s: code = %d, want 404", bad, r.Code)
		}
	}
}

func TestStampAuthenticated(t *testing.T) {
	// Flips the boot marker to the logged-in state, exactly once.
	out := string(ui.StampAuthenticated([]byte(`<body data-auth="loading"><div data-auth="loading"></div>`)))
	if !strings.Contains(out, `<body data-auth="in">`) {
		t.Errorf("body marker not flipped to in: %s", out)
	}
	if strings.Count(out, `data-auth="loading"`) != 1 {
		t.Errorf("only the first marker (the body) should flip, got: %s", out)
	}

	// Absent marker is a no-op (already stamped, or an unexpected shell).
	none := []byte(`<body data-auth="out">`)
	if got := string(ui.StampAuthenticated(none)); got != string(none) {
		t.Errorf("no marker must be a no-op, got %q", got)
	}
}

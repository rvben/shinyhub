package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeaders_ControlPlane(t *testing.T) {
	h := SecurityHeaders(nil, nil, nil, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, path := range []string{"/", "/login", "/apps/foo/overview", "/api/apps", "/static/app.js"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		hd := rec.Header()
		if hd.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing X-Content-Type-Options=nosniff", path)
		}
		if hd.Get("X-Frame-Options") != "SAMEORIGIN" {
			t.Errorf("%s: missing X-Frame-Options=SAMEORIGIN (clickjacking)", path)
		}
		if hd.Get("Referrer-Policy") != "same-origin" {
			t.Errorf("%s: missing Referrer-Policy=same-origin", path)
		}
		if hd.Get("Permissions-Policy") == "" {
			t.Errorf("%s: missing Permissions-Policy", path)
		}
		// HSTS must NOT be sent over plain HTTP (the browser ignores it there, and
		// asserting a clean policy keeps the scheme gating honest).
		if hd.Get("Strict-Transport-Security") != "" {
			t.Errorf("%s: HSTS sent over plain HTTP: %q", path, hd.Get("Strict-Transport-Security"))
		}
		csp := hd.Get("Content-Security-Policy")
		if !strings.Contains(csp, "frame-ancestors 'self'") {
			t.Errorf("%s: CSP missing frame-ancestors 'self': %q", path, csp)
		}
		if !strings.Contains(csp, "default-src 'self'") {
			t.Errorf("%s: CSP missing default-src 'self': %q", path, csp)
		}
		// The dashboard pulls fonts from Google Fonts; the CSP must permit that
		// or the UI breaks.
		if !strings.Contains(csp, "fonts.gstatic.com") || !strings.Contains(csp, "fonts.googleapis.com") {
			t.Errorf("%s: CSP must allow Google Fonts hosts or the dashboard fonts break: %q", path, csp)
		}
		// No 'unsafe-inline': inline scripts/styles are allowed by hash, not blanket.
		if strings.Contains(csp, "'unsafe-inline'") {
			t.Errorf("%s: CSP must not contain 'unsafe-inline': %q", path, csp)
		}
	}
}

// TestSecurityHeaders_BrandingImageSources pins the img-src widening that lets a
// remotely-hosted operator logo actually load. Without it the default
// `img-src 'self' data:` blocks the image the operator configured.
func TestSecurityHeaders_BrandingImageSources(t *testing.T) {
	h := SecurityHeaders(nil, nil, nil, []string{"https://cdn.example.com"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src 'self' data: https://cdn.example.com;") {
		t.Errorf("configured branding image origin must be allowed in img-src: %q", csp)
	}
	// The widening is scoped to images: nothing else may inherit the origin.
	if strings.Contains(csp, "script-src 'self' https://cdn.example.com") ||
		strings.Contains(csp, "connect-src 'self' https://cdn.example.com") {
		t.Errorf("branding image origin must widen img-src only: %q", csp)
	}
}

// TestBuildControlPlaneCSP asserts the policy is strict with no branding and
// lists the exact inline hashes (never 'unsafe-inline') when branding is active.
func TestBuildControlPlaneCSP(t *testing.T) {
	plain := buildControlPlaneCSP(nil, nil, nil)
	if !strings.Contains(plain, "img-src 'self' data:;") {
		t.Errorf("with no branding the image policy must stay closed: %q", plain)
	}
	if strings.Contains(plain, "'unsafe-inline'") {
		t.Errorf("inactive-branding CSP must not contain 'unsafe-inline': %q", plain)
	}
	if !strings.Contains(plain, "script-src 'self';") {
		t.Errorf("inactive-branding script-src should be just 'self': %q", plain)
	}

	csp := buildControlPlaneCSP([]string{"'sha256-abc'"}, []string{"'sha256-def'"}, nil)
	if !strings.Contains(csp, "script-src 'self' 'sha256-abc'") {
		t.Errorf("script-src missing hash source: %q", csp)
	}
	if !strings.Contains(csp, "style-src 'self' 'sha256-def' https://fonts.googleapis.com") {
		t.Errorf("style-src missing hash source or fonts host: %q", csp)
	}
	if strings.Contains(csp, "'unsafe-inline'") {
		t.Errorf("active-branding CSP must not contain 'unsafe-inline': %q", csp)
	}
}

// TestLandingPageCSP asserts the operator landing-page policy permits inline
// scripts/styles (trusted operator HTML), unlike the strict SPA policy.
func TestLandingPageCSP(t *testing.T) {
	csp := LandingPageCSP()
	if !strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("landing-page CSP must permit inline scripts: %q", csp)
	}
	if !strings.Contains(csp, "style-src 'self' 'unsafe-inline'") {
		t.Errorf("landing-page CSP must permit inline styles: %q", csp)
	}
}

func TestSecurityHeaders_HSTSOverHTTPS(t *testing.T) {
	h := SecurityHeaders(nil, nil, nil, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	// A direct TLS request (httptest sets req.TLS for an https target).
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://host/", nil))
	hsts := rec.Header().Get("Strict-Transport-Security")
	if !strings.Contains(hsts, "max-age=") {
		t.Errorf("HTTPS request missing HSTS max-age: %q", hsts)
	}
	if !strings.Contains(hsts, "includeSubDomains") {
		t.Errorf("HSTS missing includeSubDomains: %q", hsts)
	}
}

func TestSecurityHeaders_ProxiedAppsUntouched(t *testing.T) {
	h := SecurityHeaders(nil, nil, nil, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://host/app/dash/", nil))
	hd := rec.Header()
	// Proxied app responses must not inherit the control-plane framing/CSP policy:
	// apps may legitimately be embedded and run their own inline scripts/styles.
	if hd.Get("X-Frame-Options") != "" {
		t.Errorf("/app/ must not get X-Frame-Options (apps may be embedded), got %q", hd.Get("X-Frame-Options"))
	}
	if hd.Get("Content-Security-Policy") != "" {
		t.Errorf("/app/ must not get the control-plane CSP, got %q", hd.Get("Content-Security-Policy"))
	}
	if hd.Get("Strict-Transport-Security") != "" {
		t.Errorf("/app/ must not get HSTS, got %q", hd.Get("Strict-Transport-Security"))
	}
}

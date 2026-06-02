package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeaders_ControlPlane(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}
}

func TestSecurityHeaders_ProxiedAppsUntouched(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app/dash/", nil))
	hd := rec.Header()
	// Proxied app responses must not inherit the control-plane framing/CSP policy:
	// apps may legitimately be embedded and run their own inline scripts/styles.
	if hd.Get("X-Frame-Options") != "" {
		t.Errorf("/app/ must not get X-Frame-Options (apps may be embedded), got %q", hd.Get("X-Frame-Options"))
	}
	if hd.Get("Content-Security-Policy") != "" {
		t.Errorf("/app/ must not get the control-plane CSP, got %q", hd.Get("Content-Security-Policy"))
	}
}

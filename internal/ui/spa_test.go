package ui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/ui"
)

func TestSPAHandler_ServesIndexForUIRoutes(t *testing.T) {
	cases := []string{
		"/apps/replica-smoke",
		"/apps/replica-smoke/logs",
		"/apps/replica-smoke/deployments",
		"/apps/replica-smoke/configuration",
		"/apps/replica-smoke/data",
		"/apps/replica-smoke/access",
		"/users",
		"/audit-log",
	}
	h := ui.SPAHandler()
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest("GET", path, nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rr.Code)
			}
			ct := rr.Header().Get("Content-Type")
			if !strings.HasPrefix(ct, "text/html") {
				t.Fatalf("content-type = %q, want text/html", ct)
			}
			if !strings.Contains(rr.Body.String(), "<title>ShinyHub</title>") {
				t.Fatalf("body does not contain index.html marker")
			}
		})
	}
}

func TestSPAHandler_404sUnknownPaths(t *testing.T) {
	h := ui.SPAHandler()
	for _, path := range []string{
		"/nope",
		"/apps",           // bare /apps is not a UI route (list lives at /)
		"/apps/",          // trailing slash without slug
		"/favicon.ico",    // static asset, not a UI route
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest("GET", path, nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rr.Code)
			}
		})
	}
}

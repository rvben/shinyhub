package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/proxy"
)

// A request for a crashed app (no live backend) gets a clear status page with the
// failure reason and a dashboard link - not the endlessly-retrying loading page.
func TestServeMissPage_CrashedShowsReason(t *testing.T) {
	p := proxy.New()
	p.SetPoolSize("broke", 1) // sized but no registered backend -> a miss
	p.SetAppStatusLookup(func(slug string) (string, string) {
		if slug == "broke" {
			return "crashed", "ModuleNotFoundError: No module named 'pandas'"
		}
		return "", ""
	})

	req := httptest.NewRequest(http.MethodGet, "/app/broke/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "This app crashed") {
		t.Error("crash page missing the heading")
	}
	if !strings.Contains(body, "ModuleNotFoundError") {
		t.Error("crash page missing the failure reason")
	}
	if strings.Contains(body, `id="shinyhub-box"`) {
		t.Error("crashed app served the auto-retrying loading spinner instead of the crash page")
	}
	if !strings.Contains(body, `href="/apps/broke"`) {
		t.Error("crash page must link to the dashboard for Restart")
	}
}

// A stopped app gets a clear "stopped" page rather than the spinner.
func TestServeMissPage_StoppedShowsStopped(t *testing.T) {
	p := proxy.New()
	p.SetPoolSize("idle", 1)
	p.SetAppStatusLookup(func(_ string) (string, string) { return "stopped", "" })

	req := httptest.NewRequest(http.MethodGet, "/app/idle/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "This app is stopped") {
		t.Error("stopped page missing its message")
	}
}

// A hibernated app is mid-wake, so it keeps the auto-retrying loading page.
func TestServeMissPage_HibernatedServesLoadingPage(t *testing.T) {
	p := proxy.New()
	p.SetPoolSize("warm", 1)
	p.SetAppStatusLookup(func(_ string) (string, string) { return "hibernated", "" })

	req := httptest.NewRequest(http.MethodGet, "/app/warm/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (loading page)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `id="shinyhub-box"`) {
		t.Error("hibernated app must get the loading page, not a down page")
	}
}

// The generic loading page keeps its bounded give-up after the shell refactor:
// the retry cap, the error copy, and the manual retry button must survive.
func TestLoadingPage_KeepsBoundedGiveUp(t *testing.T) {
	p := proxy.New()
	p.SetPoolSize("cold", 1) // sized but no backend, no status lookup -> loading page

	req := httptest.NewRequest(http.MethodGet, "/app/cold/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, needle := range []string{
		proxy.LoadingPageSentinel,
		"var MAX = 20",
		"App did not start",
		`id="shinyhub-retry"`,
		"window.location.reload",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("loading page missing %q", needle)
		}
	}
}

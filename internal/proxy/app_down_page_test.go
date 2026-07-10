package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// A deployment is in flight: serve the deploy-aware wait page. It must
// auto-refresh, must clear the give-up counter, and must not contain the
// give-up state that falsely reports "App did not start" mid-build.
func TestServeMissPage_DeployingServesDeployAwareWaitPage(t *testing.T) {
	p := proxy.New()
	p.SetPoolSize("ship", 1)
	p.SetAppStatusLookup(func(_ string) (string, string) { return "deploying", "" })

	req := httptest.NewRequest(http.MethodGet, "/app/ship/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, proxy.DeployingPageSentinel) {
		t.Error("deploying page missing its sentinel copy")
	}
	if !strings.Contains(body, "sessionStorage.removeItem") {
		t.Error("deploying page must clear the give-up counter")
	}
	if !strings.Contains(body, "window.location.reload") {
		t.Error("deploying page must auto-refresh")
	}
	if strings.Contains(body, "App did not start") {
		t.Error("deploying page must not carry the give-up state; a long build would falsely report failure")
	}
	if strings.Contains(body, "This app is stopped") {
		t.Error("deploying app rendered the stopped page")
	}
}

// While a deployment is in flight, neither the wake trigger nor the clustered
// on-miss sync may fire: the per-slug deploy lock owns the app. A wake could
// queue a redundant restart behind the deploy; a sync could re-register stale
// replica rows for the pool the deploy just tore down.
func TestServeMissPage_DeployingFiresNeitherWakeNorSync(t *testing.T) {
	p := proxy.New()
	p.SetPoolSize("ship", 1)
	p.SetAppStatusLookup(func(_ string) (string, string) { return "deploying", "" })
	fired := make(chan string, 2)
	p.SetWakeTrigger(func(slug string) { fired <- "wake:" + slug })
	p.SetOnMissSync(func(slug string) { fired <- "sync:" + slug })

	req := httptest.NewRequest(http.MethodGet, "/app/ship/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	select {
	case what := <-fired:
		t.Fatalf("%s fired during an in-flight deployment", what)
	case <-time.After(50 * time.Millisecond):
	}
}

// Control: a hibernated app still fires the wake trigger on a miss (the
// deploying suppression must not leak into the normal wake path).
func TestServeMissPage_HibernatedStillFiresWake(t *testing.T) {
	p := proxy.New()
	p.SetPoolSize("warm", 1)
	p.SetAppStatusLookup(func(_ string) (string, string) { return "hibernated", "" })
	fired := make(chan string, 1)
	p.SetWakeTrigger(func(slug string) { fired <- slug })

	req := httptest.NewRequest(http.MethodGet, "/app/warm/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("wake trigger did not fire for a hibernated app")
	}
}

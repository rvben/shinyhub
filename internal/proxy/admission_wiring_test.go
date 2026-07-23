package proxy

import (
	"testing"

	"github.com/rvben/shinyhub/internal/admission"
)

func TestBuildAppLimiter_DisabledWhenRenderSecondsZero(t *testing.T) {
	// render_seconds 0 means pacing off: no limiter is built.
	if l := BuildAppLimiter(0, 2, 0.75, 3, 20, 4096); l != nil {
		t.Fatal("BuildAppLimiter with render_seconds 0 must return nil (pacing disabled)")
	}
}

func TestBuildAppLimiter_BuildsWhenEnabled(t *testing.T) {
	l := BuildAppLimiter(1.3, 2, 0.75, 3, 20, 4096)
	if l == nil {
		t.Fatal("BuildAppLimiter with render_seconds > 0 must return a limiter")
	}
	// The limiter is usable: a first admit for a principal succeeds (burst).
	if !l.TryAdmit("user-1") {
		t.Fatal("a fresh limiter should admit the first request for a principal")
	}
}

func TestProxyAppLimiterRegistry(t *testing.T) {
	p := New()
	if p.appLimiter("missing") != nil {
		t.Fatal("unset slug should have no limiter")
	}
	l := BuildAppLimiter(1.3, 2, 0.75, 3, 20, 4096)
	p.SetAppLimiter("app-a", l)
	if p.appLimiter("app-a") != l {
		t.Fatal("SetAppLimiter/appLimiter should round-trip the limiter")
	}
	// A nil limiter clears the slot (pacing disabled for the app).
	p.SetAppLimiter("app-a", nil)
	if p.appLimiter("app-a") != nil {
		t.Fatal("SetAppLimiter(nil) should clear the slug's limiter")
	}
}

func TestProxyWatermarkAndAccessLookupSetters(t *testing.T) {
	p := New()
	// Setters accept and store without panicking; not yet consumed by routing.
	p.SetCPUWatermark(admission.NewWatermark(0, func() (float64, error) { return 0, nil }))
	p.SetAppAccessLookup(func(slug string) string { return "public" })
}

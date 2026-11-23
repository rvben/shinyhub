package proxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/appnav"
)

const navPagesHome = "https://hub.example.com/"

// missPageSurfaces are the pages ShinyHub serves in an app's place. Each one is
// a dead end by construction: the app the visitor asked for is not there, so the
// page is all they have. They are enumerated here rather than tested one by one
// because the failure this guards against is a new surface being added without
// the switcher, and a table is the only shape where adding a surface is visibly
// a decision.
var missPageSurfaces = []struct {
	name    string
	setup   func(t *testing.T, p *Proxy)
	target  string
	sentine string // copy unique to this page, so a test cannot pass on the wrong one
	// refreshes marks the pages that reload themselves while the visitor waits.
	// Those must never be stored; the terminal pages (crashed, stopped) do not
	// reload and carry no such header today.
	refreshes bool
}{
	{
		name: "crashed",
		setup: func(_ *testing.T, p *Proxy) {
			p.SetPoolSize("demo", 1)
			p.SetAppStatusLookup(func(string) (string, string) { return "crashed", "boom" })
		},
		target:  "/app/demo/",
		sentine: "This app crashed",
	},
	{
		name: "stopped",
		setup: func(_ *testing.T, p *Proxy) {
			p.SetPoolSize("demo", 1)
			p.SetAppStatusLookup(func(string) (string, string) { return "stopped", "" })
		},
		target:  "/app/demo/",
		sentine: "This app is stopped",
	},
	{
		name: "starting",
		setup: func(_ *testing.T, p *Proxy) {
			p.SetSlugExists(func(string) (bool, error) { return true, nil })
			p.SetWakeHoldTimeout(10 * time.Millisecond)
		},
		target:    "/app/demo/",
		sentine:   LoadingPageSentinel,
		refreshes: true,
	},
	{
		name: "deploying",
		setup: func(_ *testing.T, p *Proxy) {
			p.SetSlugExists(func(string) (bool, error) { return true, nil })
			p.SetAppStatusLookup(func(string) (string, string) { return "deploying", "" })
			p.SetWakeHoldTimeout(10 * time.Millisecond)
		},
		target:    "/app/demo/",
		sentine:   DeployingPageSentinel,
		refreshes: true,
	},
	{
		name: "at capacity",
		setup: func(t *testing.T, p *Proxy) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Write([]byte("real app content")) //nolint:errcheck
			}))
			t.Cleanup(backend.Close)
			if err := p.Register("demo", backend.URL); err != nil {
				t.Fatal(err)
			}
			p.SetAppLimiter("demo", emptyLimiter())
		},
		target:    "/app/demo/",
		sentine:   WaitingPageSentinel,
		refreshes: true,
	},
}

// serveSurface renders one miss page, optionally with the switcher enabled.
func serveSurface(t *testing.T, i int, nav bool) *httptest.ResponseRecorder {
	t.Helper()
	s := missPageSurfaces[i]
	p := New()
	s.setup(t, p)
	p.SetAppFavicon(true)
	p.SetAppNameLookup(func(string) string { return "Revenue Forecast" })
	if nav {
		p.SetAppNav(true, navPagesHome)
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, pageLoadRequest(s.target))
	if !strings.Contains(rec.Body.String(), s.sentine) {
		t.Fatalf("precondition: this is not the %s page: %.160q", s.name, rec.Body.String())
	}
	return rec
}

// Every page ShinyHub serves instead of an app must offer a way to another app.
// These are the surfaces where the visitor is most stuck: the thing they asked
// for is not there, and without the switcher the page names no alternative.
func TestMissPages_CarryTheSwitcher(t *testing.T) {
	for i, s := range missPageSurfaces {
		t.Run(s.name, func(t *testing.T) {
			body := serveSurface(t, i, true).Body.String()

			tag := strings.Index(body, `id="`+appnav.ScriptID+`"`)
			if tag < 0 {
				t.Fatalf("the %s page carries no app switcher:\n%s", s.name, body)
			}
			// Position, not just presence: spliced after the page's own content
			// and before the closing tag. A contains-check alone passes on a
			// script dropped into the head, where the page's markup is not there
			// for it to attach to yet.
			closing := strings.LastIndex(body, "</body>")
			if closing < 0 || tag > closing {
				t.Fatalf("switcher is outside the document body (tag at %d, </body> at %d)", tag, closing)
			}
			if !strings.Contains(body, `data-home-url="`+navPagesHome+`"`) {
				t.Error("switcher carries no home link, so the dashboard is unreachable from it")
			}
			if !strings.Contains(body, appnav.DataURL("demo")) {
				t.Error("switcher carries no data URL, so it can never populate")
			}
			if !strings.Contains(body, `data-current-name="Revenue Forecast"`) {
				t.Error("switcher carries no friendly current app name")
			}
			if !strings.Contains(body, `<title>Revenue Forecast · ShinyHub</title>`) {
				t.Errorf("%s page does not use the app's ShinyHub lifecycle title", s.name)
			}
		})
	}
}

// With the switcher off these pages must be what they were before it existed.
// An operator who sets app_nav: false is entitled to exactly that.
func TestMissPages_WithoutTheSwitcher_AreUnchanged(t *testing.T) {
	for i, s := range missPageSurfaces {
		t.Run(s.name, func(t *testing.T) {
			off := serveSurface(t, i, false).Body.String()
			if strings.Contains(off, appnav.ScriptID) {
				t.Fatalf("the switcher appeared on a page that never asked for it:\n%s", off)
			}
			// Positive control: the same page WITH the switcher does differ, so
			// the absence above is evidence about the setting rather than about
			// a splice that never works on this surface.
			if on := serveSurface(t, i, true).Body.String(); on == off {
				t.Fatal("enabling the switcher changed nothing, so the negative case proves nothing")
			}
		})
	}
}

// The switcher is added to ShinyHub's own pages, never to the app's response.
// An app that is up and answering owns its bytes; the injection path for those
// is the CSP-aware one, which declines far more often than this splice does.
func TestMissPages_LiveAppResponseIsNotSpliced(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck
	}))
	t.Cleanup(backend.Close)

	p := New()
	if err := p.Register("demo", backend.URL); err != nil {
		t.Fatal(err)
	}
	p.SetAppNav(true, navPagesHome)

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/app/demo/api/data", nil))

	if got := rec.Body.String(); got != `{"ok":true}` {
		t.Fatalf("a non-HTML app response was rewritten: %s", got)
	}
}

// The wait pages refresh themselves, and a shared cache that stored one would
// replay it forever. Adding the switcher must not disturb that: it is spliced
// into the body, and the headers are the page's own.
func TestMissPages_StayUncacheableWithTheSwitcher(t *testing.T) {
	for i, s := range missPageSurfaces {
		if !s.refreshes {
			continue
		}
		t.Run(s.name, func(t *testing.T) {
			rec := serveSurface(t, i, true)
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("%s page Cache-Control = %q, want no-store", s.name, got)
			}
		})
	}
}

func TestAppNavCacheRespectsPagePolicy(t *testing.T) {
	const appOrigin = "https://apps.example.com"
	tests := []struct {
		name   string
		origin string
		header string
		value  string
		meta   string
		target string
		proxy  bool
		mode   string
	}{
		{name: "dedicated app origin", origin: appOrigin, mode: "external"},
		{name: "default", mode: "inline"},
		{name: "different host", origin: appOrigin, target: "https://alias.example.com", mode: "inline"},
		{name: "different scheme", origin: appOrigin, target: "http://apps.example.com", mode: "inline"},
		{name: "default TLS port", origin: appOrigin + ":443", target: appOrigin, mode: "external"},
		{name: "trusted TLS termination", origin: appOrigin, target: "http://internal.example.com", proxy: true, mode: "external"},
		{name: "untrusted forwarding", origin: appOrigin, target: "http://internal.example.com", mode: "inline"},
		{name: "script policy", origin: appOrigin, header: "Content-Security-Policy", value: "script-src 'self'", mode: "inline"},
		{name: "report-only policy", origin: appOrigin, header: "Content-Security-Policy-Report-Only", value: "script-src 'self'", mode: "inline"},
		{name: "scripts forbidden", origin: appOrigin, header: "Content-Security-Policy", value: "script-src 'none'", mode: "absent"},
		{name: "meta policy", origin: appOrigin, meta: `<meta http-equiv="Content-Security-Policy" content="script-src 'self'">`, mode: "absent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				if tt.header != "" {
					w.Header().Set(tt.header, tt.value)
				}
				_, _ = w.Write([]byte("<html><head>" + tt.meta + "</head><body>App</body></html>"))
			}))
			t.Cleanup(backend.Close)
			p := New()
			if err := p.Register("demo", backend.URL); err != nil {
				t.Fatal(err)
			}
			p.SetAppNavForOrigin(true, navPagesHome, tt.origin)
			rec := httptest.NewRecorder()
			target := tt.target
			if target == "" {
				target = appOrigin
			}
			req := pageLoadRequest(target + "/app/demo/")
			req.Header.Set("X-Forwarded-Host", "apps.example.com")
			req.Header.Set("X-Forwarded-Proto", "https")
			if tt.proxy {
				_, network, err := net.ParseCIDR("192.0.2.0/24")
				if err != nil {
					t.Fatal(err)
				}
				p.SetTrustedProxies([]*net.IPNet{network})
				req.RemoteAddr = "192.0.2.4:12345"
			}
			p.ServeHTTP(rec, req)
			body := rec.Body.String()
			external := strings.Contains(body, `src="`+tt.origin+`/app/.shinyhub/nav/`) &&
				strings.Contains(body, `" async></script></body>`)
			inline := strings.Contains(body, appnav.Script)
			tag := strings.Contains(body, appnav.ScriptID)
			if external != (tt.mode == "external") || inline != (tt.mode == "inline") || tag != (tt.mode != "absent") {
				t.Fatalf("switcher delivery got external=%t, inline=%t, tag=%t, want %s", external, inline, tag, tt.mode)
			}
		})
	}
}

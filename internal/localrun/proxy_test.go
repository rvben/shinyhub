package localrun

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/appnav"
)

func TestLocalProxyCanonicalizesAppRootBeforeBackendStarts(t *testing.T) {
	lp, err := newLocalProxy(0, "demo")
	if err != nil {
		t.Fatal(err)
	}
	defer lp.close()
	rr := httptest.NewRecorder()
	lp.server.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/app/demo?_inputs_&x=%2f", nil))
	if rr.Code != http.StatusPermanentRedirect || rr.Header().Get("Location") != "/app/demo/?_inputs_&x=%2f" {
		t.Fatalf("local app root: status=%d Location=%q", rr.Code, rr.Header().Get("Location"))
	}
}

func TestLocalProxyEnablesAppChromeWithCurrentAppNavigation(t *testing.T) {
	lp, err := newLocalProxy(0, "bookmark-demo")
	if err != nil {
		t.Fatal(err)
	}
	defer lp.close()

	if !lp.proxy.AppNavEnabled() {
		t.Fatal("local proxy did not enable injected app navigation")
	}

	req := httptest.NewRequest(http.MethodGet, appnav.DataURL("bookmark-demo"), nil)
	rr := httptest.NewRecorder()
	lp.server.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("nav status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var payload appnav.Payload
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Apps) != 1 || payload.Apps[0].Slug != "bookmark-demo" || !payload.Apps[0].Openable {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestBrowserRevisionChangesOnlyOnActivation(t *testing.T) {
	lp, err := newLocalProxy(0, "demo")
	if err != nil {
		t.Fatal(err)
	}
	defer lp.close()
	lp.enableBrowserRefresh()
	unavailable := httptest.NewRecorder()
	lp.server.Handler.ServeHTTP(unavailable, httptest.NewRequest("GET", lp.reloadURL(), nil))
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("before first readiness = %d", unavailable.Code)
	}
	lp.activateBrowserRevision()
	revision := func() string {
		t.Helper()
		rr := httptest.NewRecorder()
		lp.server.Handler.ServeHTTP(rr, httptest.NewRequest("GET", lp.reloadURL(), nil))
		if rr.Code != http.StatusOK || rr.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("revision endpoint: %d %v", rr.Code, rr.Header())
		}
		return rr.Body.String()
	}
	initial := revision()
	if err := lp.routeTo(12345); err != nil {
		t.Fatal(err)
	}
	if revision() != initial {
		t.Fatal("provisional route refreshed the browser")
	}
	if err := lp.routeTo(12346); err != nil {
		t.Fatal(err)
	}
	if revision() != initial {
		t.Fatal("rollback refreshed the browser")
	}
	lp.activateBrowserRevision()
	if revision() == initial {
		t.Fatal("healthy activation did not refresh the browser")
	}
	rr := httptest.NewRecorder()
	lp.server.Handler.ServeHTTP(rr, httptest.NewRequest("POST", lp.reloadURL(), nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d", rr.Code)
	}
}

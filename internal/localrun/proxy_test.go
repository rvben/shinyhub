package localrun

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/appnav"
)

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

package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestForwardAuthCookieCheckPreservesRawAppQuery(t *testing.T) {
	const app = "_inputs_&q=a%20b&x=%2f&state=a;b&x=2"
	r := httptest.NewRequest(http.MethodGet, "https://apps.example.com/app/demo/?"+app+"&__shinyhub_cookie_check=1&__shinyhub%5fcookie_check=other", nil)
	r.AddCookie(&http.Cookie{Name: SecureForwardAuthSignedOutCookie, Value: "1"})
	w := httptest.NewRecorder()
	handler := ForwardAuthMiddleware(nil, ForwardAuthConfig{Enabled: true, AppOriginHost: "apps.example.com"}, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("cookie confirmation reached backend")
	}))
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/app/demo/?"+app {
		t.Fatalf("confirmation: status=%d Location=%q", w.Code, w.Header().Get("Location"))
	}
	frame := httptest.NewRecorder()
	forwardFrameUnavailable(frame, r)
	if !strings.Contains(frame.Body.String(), `href="/app/demo/?_inputs_&amp;q=a%20b&amp;x=%2f&amp;state=a;b&amp;x=2"`) {
		t.Fatalf("frame link lost query: %s", frame.Body.String())
	}
}

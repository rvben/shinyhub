package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/rvben/shinyhub/internal/oauth"
)

func TestOAuthReturnPathBoundToLoginState(t *testing.T) {
	for _, provider := range []string{"github", "google", "oidc"} {
		for _, next := range []string{"/app/demo/?_inputs_&q=a%20b&x=%2f#plot", "/apps/demo?tab=logs", "", "https://evil.example/", "//evil.example/", "/%2fevil.example/", "/%5cevil.example/", "/login?next=/app/demo/", "/bad\npath"} {
			t.Run(provider+"/"+next, func(t *testing.T) {
				srv, _ := e2eTestServer(t)
				var claims map[string]any
				switch provider {
				case "github":
					srv.SetGitHubProvider(newFakeGitHub(t, nil, `{"id":123,"login":"user"}`, ""))
				case "google":
					srv.SetGoogleProvider(newFakeGoogle(t, nil, `{"id":"123","email":"user@example.test","verified_email":true}`))
				case "oidc":
					claims = map[string]any{"sub": "subject", "name": "User"}
					idp := newMockIdP(t, "shinyhub", claims)
					p, err := oauth.NewOIDCProvider(context.Background(), idp.URL, "shinyhub", "secret", "http://app.example.test/api/auth/oidc/callback", "SSO", "groups", "")
					if err != nil {
						t.Fatal(err)
					}
					srv.SetOIDCProvider(p)
				}
				login := httptest.NewRecorder()
				srv.Router().ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/api/auth/"+provider+"/login?next="+url.QueryEscape(next), nil))
				loc, err := login.Result().Location()
				if err != nil {
					t.Fatal(err)
				}
				if claims != nil {
					claims["nonce"] = loc.Query().Get("nonce")
				}
				req := httptest.NewRequest(http.MethodGet, "/api/auth/"+provider+"/callback?state="+url.QueryEscape(loc.Query().Get("state"))+"&code=mock-code&next=%2Fattacker", nil)
				for _, c := range login.Result().Cookies() {
					req.AddCookie(c)
				}
				callback := httptest.NewRecorder()
				srv.Router().ServeHTTP(callback, req)
				want := "/"
				if next == "/apps/demo?tab=logs" || next == "/app/demo/?_inputs_&q=a%20b&x=%2f#plot" {
					want = next
				}
				if callback.Code != http.StatusFound || callback.Header().Get("Location") != want || sessionCookie(callback) == nil {
					t.Fatalf("callback: status=%d Location=%q body=%s, want %q", callback.Code, callback.Header().Get("Location"), callback.Body.String(), want)
				}
				// The return destination must not make a state replay reusable.
				replay := httptest.NewRecorder()
				srv.Router().ServeHTTP(replay, req)
				if replay.Code != http.StatusBadRequest {
					t.Fatalf("replay status=%d", replay.Code)
				}
			})
		}
	}
}

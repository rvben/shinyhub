package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
)

func TestAppOriginLaunchPreservesRawAppQuery(t *testing.T) {
	origin, _ := url.Parse("https://apps.example.com")
	for _, signedIn := range []bool{false, true} {
		for _, query := range []string{"", "?", "?_inputs_&q=a%20b&x=%2f&state=a;b&bad=%ZZ&x=2", "?_inputs_&__shinyhub_launch=old&x=%2f&__shinyhub%5flaunch=other"} {
			t.Run(query, func(t *testing.T) {
				user := &auth.ContextUser{ID: 42, Username: "user", Role: "viewer"}
				store := &fakeAppLaunchStore{user: user}
				req := httptest.NewRequest(http.MethodGet, "https://hub.example.com/app/demo/"+query, nil)
				if signedIn {
					req = req.WithContext(auth.WithUser(req.Context(), user))
				}
				w := httptest.NewRecorder()
				appOriginRedirectHandler(store, origin, nil).ServeHTTP(w, req)
				loc, err := url.Parse(w.Header().Get("Location"))
				if err != nil {
					t.Fatal(err)
				}
				want := strings.TrimPrefix(query, "?")
				if strings.Contains(query, "__shinyhub_launch") {
					want = "_inputs_&x=%2f"
				}
				if signedIn {
					code := loc.Query().Get(appLaunchQueryParam)
					if code == "" {
						t.Fatal("missing launch code")
					}
					clean := httptest.NewRecorder()
					consumeAppLaunch(clean, httptest.NewRequest(http.MethodGet, loc.String(), nil), store, "test-secret", nil, code)
					loc, err = url.Parse(clean.Header().Get("Location"))
					if err != nil || clean.Code != http.StatusSeeOther {
						t.Fatalf("exchange status=%d: %s", clean.Code, clean.Body.String())
					}
				}
				if loc.RawQuery != want || !signedIn && query == "?" && !loc.ForceQuery {
					t.Fatalf("raw query=%q force=%v, want %q", loc.RawQuery, loc.ForceQuery, want)
				}
			})
		}
	}
}

package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

func TestOIDCAuthorizationRefusalRevokesLinkedSessions(t *testing.T) {
	for _, missing := range []string{"absent", "empty", "malformed"} {
		t.Run(missing, func(t *testing.T) {
			claims := map[string]any{"sub": "linked-sub", "roles": []string{"power"}}
			srv, store, _ := oidcMigrationServer(t, claims, config.OIDCConfig{GroupsClaim: "roles", MissingGroups: "reject", RequireGroups: true})
			first := oidcMigrationLogin(t, srv, claims)
			cookie := oidcMigrationCookie(t, first)
			user, err := store.GetUserByOAuthAccount("oidc", "linked-sub")
			if err != nil {
				t.Fatal(err)
			}
			switch missing {
			case "absent":
				delete(claims, "roles")
			case "empty":
				claims["roles"] = []string{}
			case "malformed":
				claims["roles"] = 42
			}
			refused := oidcMigrationLogin(t, srv, claims)
			want, epoch := http.StatusForbidden, user.TokenEpoch+1
			if missing == "malformed" {
				want, epoch = http.StatusBadGateway, user.TokenEpoch
			}
			if refused.Code != want {
				t.Fatalf("refusal: %d %s", refused.Code, refused.Body.String())
			}
			after, err := store.GetUserByID(user.ID)
			if err != nil || after.TokenEpoch != epoch {
				t.Fatalf("epoch: %+v %v", after, err)
			}
			req := httptest.NewRequest("GET", "/api/auth/me", nil)
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)
			want = http.StatusUnauthorized
			if missing == "malformed" {
				want = http.StatusOK
			}
			if rec.Code != want {
				t.Fatalf("old session: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

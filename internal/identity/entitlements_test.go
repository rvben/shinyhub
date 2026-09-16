package identity

import (
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rvben/shinyhub/internal/auth"
)

func TestEntitlementsAreAppScopedAndSeparateFromGroups(t *testing.T) {
	p := NewProvider("secret", &fakeGroups{groups: []string{"finance"}})
	u := &auth.ContextUser{ID: 1, Username: "analyst", Role: "viewer", EntitlementAppID: 42, Entitlements: []string{"power_user"}}
	for _, appID := range []int64{42, 43} {
		pl := p.PayloadFor(u, "report", appID)
		claims := &TokenClaims{}
		if _, err := jwt.ParseWithClaims(pl.Token, claims, func(*jwt.Token) (any, error) { return DeriveKey("secret", appID), nil }, jwt.WithAudience("report"), jwt.WithIssuer(Issuer)); err != nil {
			t.Fatal(err)
		}
		if pl.GroupsHeader != "finance" || claims.Role != "viewer" || claims.AppRole != "viewer" {
			t.Fatalf("entitlement altered groups or management roles: %+v", claims)
		}
		if appID == 42 && (len(claims.Entitlements) != 1 || claims.Entitlements[0] != "power_user") {
			t.Fatalf("missing entitlement: %+v", claims)
		}
		if appID == 43 && len(claims.Entitlements) != 0 {
			t.Fatal("entitlement leaked to another app")
		}
	}
}

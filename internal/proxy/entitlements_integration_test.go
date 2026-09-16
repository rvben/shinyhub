package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/identity"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestEntitlementRevocationClosesLiveWebSocket(t *testing.T) {
	c := newReauthChain(t, "private")
	app, err := c.store.GetAppBySlug("rep")
	if err != nil {
		t.Fatal(err)
	}
	audit := db.AuditEventParams{UserID: &app.OwnerID}
	if err := c.store.DefineAppEntitlement(app.ID, db.DefineAppEntitlementParams{Name: "power_user"}, audit); err != nil {
		t.Fatal(err)
	}
	principal := db.EntitlementPrincipal{UserID: c.ana.ID}
	if err := c.store.SetAppEntitlementGrant(app.ID, "power_user", principal, true, audit); err != nil {
		t.Fatal(err)
	}
	session := c.openSession(t, c.sessionCookie(t))
	if closed := c.sweep(); closed != 0 {
		t.Fatalf("unchanged snapshot closed %d sessions", closed)
	}
	session.echo("before-revoke")
	if err := c.store.SetAppEntitlementGrant(app.ID, "power_user", principal, false, audit); err != nil {
		t.Fatal(err)
	}
	if closed := c.sweep(); closed != 1 {
		t.Fatalf("revocation closed %d sessions, want 1", closed)
	}
	session.assertClosed()
	// User retains admission and can reconnect with the reduced privilege set.
	next := c.openSession(t, c.sessionCookie(t))
	next.echo("after-revoke")
	if closed := c.sweep(); closed != 0 {
		t.Fatalf("fresh session was closed: %d", closed)
	}
}

func TestEntitlementsRefreshOnHTTPWithoutIdentityCacheLag(t *testing.T) {
	c := newReauthChain(t, "private")
	app, err := c.store.GetAppBySlug("rep")
	if err != nil {
		t.Fatal(err)
	}
	audit := db.AuditEventParams{UserID: &app.OwnerID}
	if err := c.store.DefineAppEntitlement(app.ID, db.DefineAppEntitlementParams{Name: "power_user"}, audit); err != nil {
		t.Fatal(err)
	}
	backend, headers := startChainBackend(t)
	prx := proxy.New()
	prx.SetPoolAppID(app.Slug, app.ID)
	prx.SetPoolIdentityHeaders(app.Slug, true)
	provider := identity.NewProvider(reauthSecret, c.store)
	prx.SetIdentityProvider(provider.PayloadFor)
	if err := prx.Register(app.Slug, backend.URL); err != nil {
		t.Fatal(err)
	}
	handler := access.Middleware(c.store, reauthSecret, c.store.IsTokenRevoked, c.store.LookupContextUser)(prx)
	cookie := c.sessionCookie(t)
	for _, grant := range []bool{true, false} {
		if err := c.store.SetAppEntitlementGrant(app.ID, "power_user", db.EntitlementPrincipal{UserID: c.ana.ID}, grant, audit); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "/app/rep/", nil)
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
		// A client cannot overwrite the claim by supplying platform headers.
		r.Header.Set(identity.HeaderToken, "forged")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("request: %d %s", w.Code, w.Body.String())
		}
		claims := &identity.TokenClaims{}
		if _, err := jwt.ParseWithClaims(headers().Get(identity.HeaderToken), claims, func(*jwt.Token) (any, error) { return identity.DeriveKey(reauthSecret, app.ID), nil }, jwt.WithAudience(app.Slug), jwt.WithIssuer(identity.Issuer)); err != nil {
			t.Fatal(err)
		}
		if (len(claims.Entitlements) == 1) != grant {
			t.Fatalf("grant=%v got stale entitlements %v", grant, claims.Entitlements)
		}
	}
}

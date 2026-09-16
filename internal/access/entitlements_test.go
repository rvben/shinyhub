package access_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

type unavailableEntitlements struct{ *db.Store }

func (s unavailableEntitlements) AppEntitlementsForUser(int64, int64) ([]string, error) {
	return nil, errors.New("database unavailable")
}

func TestEntitlementSnapshotRefreshAndOutage(t *testing.T) {
	f := newRecheckFixture(t)
	app, err := f.store.GetAppBySlug("rep")
	if err != nil {
		t.Fatal(err)
	}
	audit := db.AuditEventParams{UserID: &f.owner.ID}
	if err := f.store.DefineAppEntitlement(app.ID, db.DefineAppEntitlementParams{Name: "power_user"}, audit); err != nil {
		t.Fatal(err)
	}
	p := f.principal()
	p.EntitlementAppID, p.EntitlementFingerprint = app.ID, auth.EntitlementFingerprint(nil)
	f.assertKept(t, p, "no entitlements yet")
	if err := f.store.SetAppEntitlementGrant(app.ID, "power_user", db.EntitlementPrincipal{Group: "finance"}, true, audit); err != nil {
		t.Fatal(err)
	}
	f.assertKept(t, p, "a group the user does not belong to")
	if err := f.store.ReplaceUserGroups(f.ana.ID, []string{"finance"}); err != nil {
		t.Fatal(err)
	}
	f.assertClosed(t, p, "grant refreshes the session")
	p.EntitlementFingerprint = auth.EntitlementFingerprint([]string{"power_user"})
	f.assertKept(t, p, "new snapshot")
	revoked, reason, err := access.Recheck(unavailableEntitlements{f.store}, f.store.LookupContextUser, nil, p)
	if !revoked || err != nil || reason == "" {
		t.Fatalf("privileged session kept during outage: %v %q %v", revoked, reason, err)
	}
	if err := f.store.ReplaceUserGroups(f.ana.ID, nil); err != nil {
		t.Fatal(err)
	}
	f.assertClosed(t, p, "group removal revokes app privilege despite continued admission")
	// The entitlement-only fallback sweep must make the same decision even
	// when an operator disabled general session rechecks.
	revoked, _, err = access.RecheckEntitlements(f.store, p)
	if !revoked || err != nil {
		t.Fatalf("entitlement-only sweep: %v %v", revoked, err)
	}
}

func TestEntitlementLookupFailureRefusesAuthenticatedRequest(t *testing.T) {
	f := newRecheckFixture(t)
	token, err := auth.IssueJWT(f.ana.ID, f.ana.Username, f.ana.Role, "secret")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	handler := access.Middleware(unavailableEntitlements{f.store}, "secret", nil, f.store.LookupContextUser)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	r := httptest.NewRequest("GET", "/app/rep/", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || called {
		t.Fatalf("lookup failure admitted request: %d called=%v", w.Code, called)
	}
}

func TestEntitlementLookupFailureDoesNotMaskSessionRevocation(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		revoke       func(*recheckFixture, access.Principal) error
	}{
		{"epoch", "sessions revoked", func(f *recheckFixture, _ access.Principal) error { return f.store.BumpTokenEpoch(f.ana.ID) }},
		{"logout", "session signed out", func(f *recheckFixture, p access.Principal) error {
			return f.store.RevokeToken(p.JTI, f.ana.ID, time.Now().Add(time.Hour))
		}},
		{"admission", "access to the app was removed", func(f *recheckFixture, _ access.Principal) error { return f.store.RevokeAppAccess("rep", f.ana.ID) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecheckFixture(t)
			app, err := f.store.GetAppBySlug("rep")
			if err != nil {
				t.Fatal(err)
			}
			p := f.principal()
			p.EntitlementAppID, p.EntitlementFingerprint = app.ID, auth.EntitlementFingerprint(nil)
			st := unavailableEntitlements{f.store}
			// Without a revocation, preserve availability and report the error.
			if closed, _, err := access.Recheck(st, f.store.LookupContextUser, f.store.IsTokenRevoked, p); closed || err == nil {
				t.Fatalf("unchanged empty snapshot: closed=%v err=%v", closed, err)
			}
			if err := tc.revoke(f, p); err != nil {
				t.Fatal(err)
			}
			closed, reason, err := access.Recheck(st, f.store.LookupContextUser, f.store.IsTokenRevoked, p)
			if !closed || reason != tc.reason || err != nil {
				t.Fatalf("revocation masked by entitlement error: closed=%v reason=%q err=%v", closed, reason, err)
			}
		})
	}
}

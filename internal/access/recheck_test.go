package access_test

import (
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// The scenario every test here varies: user "ana" (a developer) has an open
// WebSocket to the private app "rep", which she reaches as an explicit member.
// The principal is what the proxy captured at the upgrade; the store is the live
// world that has since moved on.
type recheckFixture struct {
	store *db.Store
	ana   *db.User
	owner *db.User
}

func newRecheckFixture(t *testing.T) *recheckFixture {
	t.Helper()
	store := makeStore(t)
	mustUser(t, store, "owner", "admin")
	mustUser(t, store, "ana", "developer")
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatalf("get owner: %v", err)
	}
	ana, err := store.GetUserByUsername("ana")
	if err != nil {
		t.Fatalf("get ana: %v", err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "rep", Name: "Report", OwnerID: owner.ID}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	if err := store.GrantAppAccess("rep", ana.ID); err != nil {
		t.Fatalf("grant access: %v", err)
	}
	return &recheckFixture{store: store, ana: ana, owner: owner}
}

func mustUser(t *testing.T, store *db.Store, username, role string) {
	t.Helper()
	if err := store.CreateUser(db.CreateUserParams{Username: username, PasswordHash: "h", Role: role}); err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
}

// principal is the identity ana's live connection was admitted under.
func (f *recheckFixture) principal() access.Principal {
	return access.Principal{
		Slug:         "rep",
		UserID:       f.ana.ID,
		Role:         f.ana.Role,
		SessionEpoch: f.ana.TokenEpoch,
		JTI:          "session-jti",
	}
}

func (f *recheckFixture) recheck(t *testing.T, p access.Principal) (bool, string) {
	t.Helper()
	revokedNow, reason, err := access.Recheck(f.store, f.store.LookupContextUser, f.store.IsTokenRevoked, p)
	if err != nil {
		t.Fatalf("Recheck: unexpected error: %v", err)
	}
	return revokedNow, reason
}

// assertKept is the negative control every revocation test needs: the sweep must
// leave an untouched session alone, or "closed the connection" proves nothing.
func (f *recheckFixture) assertKept(t *testing.T, p access.Principal, what string) {
	t.Helper()
	revokedNow, reason := f.recheck(t, p)
	if revokedNow {
		t.Fatalf("%s: session closed (%s), want kept", what, reason)
	}
}

func (f *recheckFixture) assertClosed(t *testing.T, p access.Principal, what string) string {
	t.Helper()
	revokedNow, reason := f.recheck(t, p)
	if !revokedNow {
		t.Fatalf("%s: session kept, want closed", what)
	}
	if reason == "" {
		t.Fatalf("%s: closed without a reason", what)
	}
	return reason
}

func TestRecheck_UnchangedSessionSurvives(t *testing.T) {
	f := newRecheckFixture(t)
	f.assertKept(t, f.principal(), "nothing changed")
}

// An admin revoking a user's sessions (or the user changing their password)
// bumps token_epoch. That is the headline case: before this sweep existed, the
// revoked user kept every WebSocket they already had open, for hours.
func TestRecheck_TokenEpochBumpClosesSession(t *testing.T) {
	f := newRecheckFixture(t)
	p := f.principal()
	f.assertKept(t, p, "before the revocation")

	if err := f.store.BumpTokenEpoch(f.ana.ID); err != nil {
		t.Fatalf("bump epoch: %v", err)
	}

	if reason := f.assertClosed(t, p, "after revoking sessions"); reason != "sessions revoked" {
		t.Errorf("reason = %q, want %q", reason, "sessions revoked")
	}
}

// Revoking one user must not disturb anyone else's live sessions.
func TestRecheck_EpochBumpLeavesOtherUsersAlone(t *testing.T) {
	f := newRecheckFixture(t)
	mustUser(t, f.store, "bob", "developer")
	bob, err := f.store.GetUserByUsername("bob")
	if err != nil {
		t.Fatalf("get bob: %v", err)
	}
	if err := f.store.GrantAppAccess("rep", bob.ID); err != nil {
		t.Fatalf("grant bob access: %v", err)
	}
	bobConn := access.Principal{Slug: "rep", UserID: bob.ID, Role: bob.Role, SessionEpoch: bob.TokenEpoch, JTI: "bob-jti"}

	if err := f.store.BumpTokenEpoch(f.ana.ID); err != nil {
		t.Fatalf("bump epoch: %v", err)
	}

	f.assertClosed(t, f.principal(), "ana after her sessions were revoked")
	f.assertKept(t, bobConn, "bob while ana was revoked")
}

func TestRecheck_UserDeletedClosesSession(t *testing.T) {
	f := newRecheckFixture(t)
	p := f.principal()
	if err := f.store.DeleteUser(f.ana.ID); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if reason := f.assertClosed(t, p, "after deleting the user"); reason != "user deleted" {
		t.Errorf("reason = %q, want %q", reason, "user deleted")
	}
}

// A role change does not bump the epoch, so nothing else would catch it. It
// matters because the app was told this role at the upgrade (X-Shinyhub-Role
// and the role claim of the identity token) and is entitled to act on it: a
// demoted admin whose WebSocket stays open keeps admin powers inside the app.
func TestRecheck_RoleChangeClosesSession(t *testing.T) {
	f := newRecheckFixture(t)
	p := f.principal()
	if err := f.store.SetManualRole(f.ana.ID, "viewer"); err != nil {
		t.Fatalf("set role: %v", err)
	}
	if reason := f.assertClosed(t, p, "after demoting the user"); reason != "role changed" {
		t.Errorf("reason = %q, want %q", reason, "role changed")
	}
}

// Logging out revokes the session token's jti rather than bumping the epoch, so
// the jti is the only thing that identifies the session that just ended.
func TestRecheck_LoggedOutSessionClosesConnection(t *testing.T) {
	f := newRecheckFixture(t)
	p := f.principal()
	if err := f.store.RevokeToken(p.JTI, f.ana.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	if reason := f.assertClosed(t, p, "after signing out"); reason != "session signed out" {
		t.Errorf("reason = %q, want %q", reason, "session signed out")
	}
}

// A logout revokes one session. The user's other tabs, signed in under a
// different token, must keep running.
func TestRecheck_LogoutLeavesOtherSessionsOfSameUserAlone(t *testing.T) {
	f := newRecheckFixture(t)
	first := f.principal()
	second := f.principal()
	second.JTI = "other-session-jti"

	if err := f.store.RevokeToken(first.JTI, f.ana.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("revoke token: %v", err)
	}

	f.assertClosed(t, first, "the session that signed out")
	f.assertKept(t, second, "the user's other session")
}

func TestRecheck_MembershipRemovalClosesSession(t *testing.T) {
	f := newRecheckFixture(t)
	p := f.principal()
	if err := f.store.RevokeAppAccess("rep", f.ana.ID); err != nil {
		t.Fatalf("revoke app access: %v", err)
	}
	if reason := f.assertClosed(t, p, "after removing the member"); reason != "access to the app was removed" {
		t.Errorf("reason = %q, want %q", reason, "access to the app was removed")
	}
}

// Group-derived access is the SSO equivalent of a membership: dropping the
// group must close the session just as removing an explicit member does.
func TestRecheck_GroupAccessRemovalClosesSession(t *testing.T) {
	f := newRecheckFixture(t)
	// Reach the app purely through a group.
	if err := f.store.RevokeAppAccess("rep", f.ana.ID); err != nil {
		t.Fatalf("revoke direct access: %v", err)
	}
	if err := f.store.ReplaceUserGroups(f.ana.ID, []string{"analysts"}); err != nil {
		t.Fatalf("set groups: %v", err)
	}
	if err := f.store.GrantAppGroupAccess("rep", "analysts", "viewer", "manual"); err != nil {
		t.Fatalf("grant group access: %v", err)
	}
	p := f.principal()
	f.assertKept(t, p, "while the group still grants access")

	if err := f.store.RevokeAppGroupAccess("rep", "analysts"); err != nil {
		t.Fatalf("revoke group access: %v", err)
	}
	f.assertClosed(t, p, "after the group lost access")
}

// Flipping an app from public to private strands connections that were admitted
// without any identity at all.
func TestRecheck_PublicToPrivateClosesAnonymousSession(t *testing.T) {
	f := newRecheckFixture(t)
	if err := f.store.SetAppAccess("rep", "public"); err != nil {
		t.Fatalf("set public: %v", err)
	}
	anon := access.Principal{Slug: "rep"}
	f.assertKept(t, anon, "anonymous on a public app")

	if err := f.store.SetAppAccess("rep", "private"); err != nil {
		t.Fatalf("set private: %v", err)
	}
	if reason := f.assertClosed(t, anon, "anonymous after the app went private"); reason != "app is no longer public" {
		t.Errorf("reason = %q, want %q", reason, "app is no longer public")
	}
}

// A public app still receives the signed-in user's forwarded identity, so a
// revocation must close the connection even though admission alone would never
// deny anyone here. This is the case the admission re-run cannot catch.
func TestRecheck_RevokedIdentityClosesSessionOnPublicApp(t *testing.T) {
	f := newRecheckFixture(t)
	if err := f.store.SetAppAccess("rep", "public"); err != nil {
		t.Fatalf("set public: %v", err)
	}
	p := f.principal()
	f.assertKept(t, p, "signed in on a public app")

	if err := f.store.BumpTokenEpoch(f.ana.ID); err != nil {
		t.Fatalf("bump epoch: %v", err)
	}
	f.assertClosed(t, p, "signed in on a public app after revocation")
}

// The request path passes an unknown slug straight through rather than denying
// it, so a vanished app row is not a lapse of this principal's access.
func TestRecheck_UnknownSlugKeepsSession(t *testing.T) {
	f := newRecheckFixture(t)
	p := f.principal()
	p.Slug = "no-such-app"
	f.assertKept(t, p, "unknown slug")
}

// errStore fails the named lookup and otherwise defers to a real store, so each
// failure mode is exercised in isolation.
type errStore struct {
	inner   *db.Store
	appErr  error
	authErr error
}

func (s errStore) GetAppBySlug(slug string) (*db.App, error) {
	if s.appErr != nil {
		return nil, s.appErr
	}
	return s.inner.GetAppBySlug(slug)
}

func (s errStore) UserCanAccessApp(slug string, userID int64) (bool, error) {
	if s.authErr != nil {
		return false, s.authErr
	}
	return s.inner.UserCanAccessApp(slug, userID)
}

// Every infrastructure failure must keep the connection. Dropping every live
// session fleet-wide on a database blip is a far worse outcome than a few more
// seconds of access for a principal already revoked.
func TestRecheck_InfrastructureErrorsKeepSession(t *testing.T) {
	f := newRecheckFixture(t)
	p := f.principal()
	boom := errors.New("database unavailable")

	cases := []struct {
		name    string
		st      errStore
		lookup  auth.UserLookup
		revoked auth.RevocationChecker
	}{
		{
			name: "app lookup fails",
			st:   errStore{inner: f.store, appErr: boom},
		},
		{
			name:   "user lookup fails",
			st:     errStore{inner: f.store},
			lookup: func(int64) (*auth.ContextUser, error) { return nil, boom },
		},
		{
			name:    "revocation check fails",
			st:      errStore{inner: f.store},
			revoked: func(string) (bool, error) { return false, boom },
		},
		{
			name: "membership check fails",
			st:   errStore{inner: f.store, authErr: boom},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookup := tc.lookup
			if lookup == nil {
				lookup = f.store.LookupContextUser
			}
			revoked := tc.revoked
			if revoked == nil {
				revoked = f.store.IsTokenRevoked
			}
			closed, reason, err := access.Recheck(tc.st, lookup, revoked, p)
			if err == nil {
				t.Fatal("want an error so the caller can tell the decision failed")
			}
			if !errors.Is(err, boom) {
				t.Errorf("err = %v, want it to wrap %v", err, boom)
			}
			if closed {
				t.Fatalf("session closed (%s) on an infrastructure error, must fail open", reason)
			}
		})
	}
}

// Recheck runs the same rule the middleware admits with. A rule that drifts
// between the two would let a live session outlive the access that granted it,
// or close one the request path would still admit.
func TestRecheck_AgreesWithAdmissionForEveryAccessLevel(t *testing.T) {
	for _, level := range []string{"public", "private", "shared"} {
		t.Run(level, func(t *testing.T) {
			f := newRecheckFixture(t)
			if err := f.store.SetAppAccess("rep", level); err != nil {
				t.Fatalf("set access: %v", err)
			}
			// A member is admitted at every level, so their session is kept.
			f.assertKept(t, f.principal(), "member on a "+level+" app")

			// A non-member is admitted only when the app is public or shared,
			// and Recheck must reach the identical verdict.
			mustUser(t, f.store, "stranger", "developer")
			stranger, err := f.store.GetUserByUsername("stranger")
			if err != nil {
				t.Fatalf("get stranger: %v", err)
			}
			p := access.Principal{Slug: "rep", UserID: stranger.ID, Role: stranger.Role}
			wantKept := level != "private"
			closed, reason := f.recheck(t, p)
			if wantKept && closed {
				t.Fatalf("non-member closed (%s) on a %s app, but admission would let them in", reason, level)
			}
			if !wantKept && !closed {
				t.Fatalf("non-member kept on a %s app, but admission would deny them", level)
			}
		})
	}
}

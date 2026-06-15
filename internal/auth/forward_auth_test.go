package auth

import (
	"bytes"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeUserStore implements ForwardAuthUserStore for tests.
type fakeUserStore struct {
	users            map[string]*ContextUser
	created          []string // usernames passed to CreateForwardAuthUser
	getErr           error    // if set, GetForwardAuthUser returns this error
	reconcileErr     error    // if set, ReconcileUserFromGroups returns this error
	getUserGroupsErr error    // if set, GetUserGroups returns this error

	// state for GetUserGroups / ReconcileUserFromGroups
	storedGroups   []string            // returned by GetUserGroups
	reconcileCalls []reconcileCallArgs // recorded calls to ReconcileUserFromGroups

	// state for SetDisplayNameFromIdP
	setNameCalls []setNameArgs // recorded calls to SetDisplayNameFromIdP
	setNameErr   error         // if set, SetDisplayNameFromIdP returns this error
}

type setNameArgs struct {
	userID int64
	name   string
}

type reconcileCallArgs struct {
	userID   int64
	groups   []string
	mappings []GroupRoleMapping
	defRole  string
}

func newFakeStore() *fakeUserStore {
	return &fakeUserStore{users: map[string]*ContextUser{}}
}

func (f *fakeUserStore) GetForwardAuthUser(username string) (*ContextUser, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	u, ok := f.users[username]
	if !ok {
		return nil, ErrUserNotFound
	}
	return u, nil
}

func (f *fakeUserStore) CreateForwardAuthUser(username, role string) (*ContextUser, error) {
	f.created = append(f.created, username)
	u := &ContextUser{ID: int64(len(f.users) + 1), Username: username, Role: role}
	f.users[username] = u
	return u, nil
}

func (f *fakeUserStore) GetUserGroups(userID int64) ([]string, error) {
	if f.getUserGroupsErr != nil {
		return nil, f.getUserGroupsErr
	}
	return f.storedGroups, nil
}

func (f *fakeUserStore) ReconcileUserFromGroups(userID int64, groups []string, mappings []GroupRoleMapping, defaultRole string) error {
	f.reconcileCalls = append(f.reconcileCalls, reconcileCallArgs{
		userID:   userID,
		groups:   groups,
		mappings: mappings,
		defRole:  defaultRole,
	})
	return f.reconcileErr
}

func (f *fakeUserStore) SetDisplayNameFromIdP(userID int64, name string) error {
	f.setNameCalls = append(f.setNameCalls, setNameArgs{userID: userID, name: name})
	if f.setNameErr != nil {
		return f.setNameErr
	}
	for _, u := range f.users {
		if u.ID == userID {
			u.DisplayName = name
		}
	}
	return nil
}

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("parse %s: %v", s, err)
	}
	return n
}

// reachedHandler records whether the inner handler ran and the user on context.
type reachedHandler struct {
	called bool
	user   *ContextUser
}

func (h *reachedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.called = true
	h.user = UserFromContext(r.Context())
	w.WriteHeader(http.StatusOK)
}

func TestForwardAuth_TrustedPeerWithHeader_ProvisionsUser(t *testing.T) {
	store := newFakeStore()
	cfg := ForwardAuthConfig{Enabled: true, UserHeader: "X-Forwarded-User", DefaultRole: "developer"}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-User", "alice")
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, r)

	if !h.called {
		t.Fatal("inner handler not called")
	}
	if h.user == nil || h.user.Username != "alice" {
		t.Fatalf("expected alice in context, got %+v", h.user)
	}
	if len(store.created) != 1 || store.created[0] != "alice" {
		t.Fatalf("expected alice provisioned, got %v", store.created)
	}
}

func TestForwardAuth_UntrustedPeer_FallsThrough(t *testing.T) {
	store := newFakeStore()
	cfg := ForwardAuthConfig{Enabled: true, UserHeader: "X-Forwarded-User", DefaultRole: "developer"}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.7:5555" // not in trusted CIDR (RFC 5737 TEST-NET-3)
	r.Header.Set("X-Forwarded-User", "mallory")
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, r)

	if !h.called {
		t.Fatal("expected middleware to fall through to inner handler")
	}
	if h.user != nil {
		t.Fatalf("expected no user attached from untrusted header, got %+v", h.user)
	}
	if len(store.created) != 0 {
		t.Fatalf("untrusted header must not provision users, created=%v", store.created)
	}
}

func TestForwardAuth_NoUserHeader_FallsThrough(t *testing.T) {
	store := newFakeStore()
	cfg := ForwardAuthConfig{Enabled: true, UserHeader: "X-Forwarded-User", DefaultRole: "developer"}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, r)

	if !h.called {
		t.Fatal("expected middleware to fall through")
	}
	if h.user != nil {
		t.Fatalf("expected no user, got %+v", h.user)
	}
}

// TestForwardAuth_GroupsHeaderPresent_ReconcilesCalled verifies that when the
// groups header is present and the group membership has changed, ReconcileUserFromGroups
// is called with the parsed groups and the configured GroupRoleMappings.
func TestForwardAuth_GroupsHeaderPresent_ReconcilesCalled(t *testing.T) {
	store := newFakeStore()
	store.users["bob"] = &ContextUser{ID: 1, Username: "bob", Role: "developer"}
	store.storedGroups = []string{"engineers"} // DB has only engineers

	mappings := []GroupRoleMapping{{Group: "shinyhub-admins", Role: "admin"}}
	cfg := ForwardAuthConfig{
		Enabled:           true,
		UserHeader:        "X-Forwarded-User",
		GroupsHeader:      "X-Forwarded-Groups",
		DefaultRole:       "developer",
		GroupRoleMappings: mappings,
	}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-User", "bob")
	r.Header.Set("X-Forwarded-Groups", "engineers,shinyhub-admins")
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, r)

	if !h.called {
		t.Fatal("inner handler not called")
	}
	if len(store.reconcileCalls) != 1 {
		t.Fatalf("expected 1 reconcile call, got %d", len(store.reconcileCalls))
	}
	call := store.reconcileCalls[0]
	if call.userID != 1 {
		t.Errorf("reconcile called for wrong user: %d", call.userID)
	}
	wantGroups := []string{"engineers", "shinyhub-admins"}
	if strings.Join(call.groups, ",") != strings.Join(wantGroups, ",") {
		t.Errorf("reconcile groups: got %v want %v", call.groups, wantGroups)
	}
	if len(call.mappings) != 1 || call.mappings[0].Group != "shinyhub-admins" {
		t.Errorf("reconcile mappings not forwarded: %v", call.mappings)
	}
	if call.defRole != "developer" {
		t.Errorf("reconcile defaultRole: got %q want developer", call.defRole)
	}
}

// TestForwardAuth_GroupsHeaderAbsent_RevokesStaleGroups verifies that when
// groups_header is configured and the header is absent from the request,
// ReconcileUserFromGroups is still called with an empty group list.
//
// When groups_header is configured, the reverse proxy is the authoritative
// source of the user's group membership on every request. An absent header
// means the user has no groups on this request - group-derived role elevation
// must be revoked. The break-glass manual_role override and the transactional
// last-admin guard inside ReconcileUserFromGroups prevent accidental admin
// lockout. The proxy MUST always send this header, empty when the user has no
// groups.
func TestForwardAuth_GroupsHeaderAbsent_RevokesStaleGroups(t *testing.T) {
	store := newFakeStore()
	store.users["carol"] = &ContextUser{ID: 2, Username: "carol", Role: "admin"}
	store.storedGroups = []string{"admins"} // user currently has admin via group

	cfg := ForwardAuthConfig{
		Enabled:           true,
		UserHeader:        "X-Forwarded-User",
		GroupsHeader:      "X-Forwarded-Groups",
		DefaultRole:       "developer",
		GroupRoleMappings: []GroupRoleMapping{{Group: "admins", Role: "admin"}},
	}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-User", "carol")
	// Deliberately NOT setting X-Forwarded-Groups - simulates removal from IdP
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, r)

	if !h.called {
		t.Fatal("inner handler not called")
	}
	if len(store.reconcileCalls) != 1 {
		t.Fatalf("absent groups header with stored groups must trigger reconcile, got %d calls", len(store.reconcileCalls))
	}
	call := store.reconcileCalls[0]
	if len(call.groups) != 0 {
		t.Errorf("reconcile must be called with empty groups to revoke elevation, got %v", call.groups)
	}
	if call.defRole != "developer" {
		t.Errorf("reconcile defaultRole: got %q want developer", call.defRole)
	}
	if len(call.mappings) != 1 || call.mappings[0].Group != "admins" {
		t.Errorf("reconcile mappings not forwarded: %v", call.mappings)
	}
}

// TestForwardAuth_GroupsHeaderAbsentNoStoredGroups_NoReconcile verifies that
// when groups_header is configured, the header is absent, and the user has no
// stored groups, ReconcileUserFromGroups is NOT called. The incoming empty list
// already matches the stored empty list, so groupsChanged short-circuits and
// there is nothing to revoke - no needless DB write.
func TestForwardAuth_GroupsHeaderAbsentNoStoredGroups_NoReconcile(t *testing.T) {
	store := newFakeStore()
	store.users["carol"] = &ContextUser{ID: 2, Username: "carol", Role: "developer"}
	// storedGroups is nil (no groups stored) - matches the incoming empty list

	cfg := ForwardAuthConfig{
		Enabled:           true,
		UserHeader:        "X-Forwarded-User",
		GroupsHeader:      "X-Forwarded-Groups",
		DefaultRole:       "developer",
		GroupRoleMappings: []GroupRoleMapping{{Group: "admins", Role: "admin"}},
	}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-User", "carol")
	// Deliberately NOT setting X-Forwarded-Groups
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, r)

	if !h.called {
		t.Fatal("inner handler not called")
	}
	if len(store.reconcileCalls) != 0 {
		t.Fatalf("no stored groups + absent header = no change, got %d reconcile calls", len(store.reconcileCalls))
	}
}

// TestForwardAuth_GroupsHeaderNotConfigured_AbsentHeader_NoReconcile verifies
// that when groups_header is NOT configured (group-driven roles are disabled),
// a user with stored groups receives no reconcile call even when the header is
// absent. Revocation only applies when groups_header is configured; otherwise
// group state is never touched by the middleware.
func TestForwardAuth_GroupsHeaderNotConfigured_AbsentHeader_NoReconcile(t *testing.T) {
	store := newFakeStore()
	store.users["carol"] = &ContextUser{ID: 2, Username: "carol", Role: "admin"}
	store.storedGroups = []string{"admins"} // stored groups, but feature disabled

	cfg := ForwardAuthConfig{
		Enabled:      true,
		UserHeader:   "X-Forwarded-User",
		GroupsHeader: "", // not configured
		DefaultRole:  "developer",
	}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-User", "carol")
	// No groups header
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, r)

	if !h.called {
		t.Fatal("inner handler not called")
	}
	if len(store.reconcileCalls) != 0 {
		t.Fatalf("groups_header not configured must never trigger reconcile, got %d calls", len(store.reconcileCalls))
	}
}

// TestForwardAuth_GroupsHeaderUnchanged_NoReconcile verifies that when the
// groups header is present but groups are unchanged (same as stored), reconcile
// is skipped to avoid unnecessary DB writes.
func TestForwardAuth_GroupsHeaderUnchanged_NoReconcile(t *testing.T) {
	store := newFakeStore()
	store.users["dave"] = &ContextUser{ID: 3, Username: "dave", Role: "developer"}
	store.storedGroups = []string{"engineers"} // same as incoming

	cfg := ForwardAuthConfig{
		Enabled:      true,
		UserHeader:   "X-Forwarded-User",
		GroupsHeader: "X-Forwarded-Groups",
		DefaultRole:  "developer",
	}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-User", "dave")
	r.Header.Set("X-Forwarded-Groups", "engineers")
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, r)

	if !h.called {
		t.Fatal("inner handler not called")
	}
	if len(store.reconcileCalls) != 0 {
		t.Fatalf("unchanged groups must not trigger reconcile, got %d calls", len(store.reconcileCalls))
	}
}

func TestForwardAuth_DisabledIsNoop(t *testing.T) {
	store := newFakeStore()
	cfg := ForwardAuthConfig{Enabled: false}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-User", "alice")
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, r)

	if h.user != nil {
		t.Fatalf("expected disabled middleware to attach no user, got %+v", h.user)
	}
	if len(store.created) != 0 {
		t.Fatal("disabled middleware must not call store")
	}
}

func TestParseGroups_TrimsAndIgnoresEmpty(t *testing.T) {
	got := parseGroups("  foo, bar ,,baz  ")
	want := []string{"foo", "bar", "baz"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("parseGroups: got %v want %v", got, want)
	}
}

func TestForwardAuth_StoreGetError_Returns500(t *testing.T) {
	store := newFakeStore()
	store.getErr = errors.New("db down")
	cfg := ForwardAuthConfig{Enabled: true, UserHeader: "X-Forwarded-User", DefaultRole: "developer"}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-User", "alice")
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, r)

	if h.called {
		t.Fatal("inner handler must not be called on store error")
	}
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}

// TestForwardAuth_ReconcileError_Returns500 verifies that a ReconcileUserFromGroups
// failure returns 500 and prevents the inner handler from running.
func TestForwardAuth_ReconcileError_Returns500(t *testing.T) {
	store := newFakeStore()
	store.reconcileErr = errors.New("update failed")
	// Pre-populate bob so GetForwardAuthUser succeeds.
	store.users["bob"] = &ContextUser{ID: 1, Username: "bob", Role: "developer"}
	// storedGroups empty so incoming groups "shinyhub-admins" triggers reconcile.
	cfg := ForwardAuthConfig{
		Enabled:      true,
		UserHeader:   "X-Forwarded-User",
		GroupsHeader: "X-Forwarded-Groups",
		DefaultRole:  "developer",
		GroupRoleMappings: []GroupRoleMapping{
			{Group: "shinyhub-admins", Role: "admin"},
		},
	}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-User", "bob")
	r.Header.Set("X-Forwarded-Groups", "shinyhub-admins")
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, r)

	if h.called {
		t.Fatal("inner handler must not be called on reconcile error")
	}
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}

// TestForwardAuth_RequireGroupsHeader_AbsentRefused verifies that in strict mode
// (RequireGroupsHeader: true), a forward-auth request that is missing the groups
// header is refused with 403, the inner handler is never called, and no
// reconcile is triggered.
func TestForwardAuth_RequireGroupsHeader_AbsentRefused(t *testing.T) {
	store := newFakeStore()
	store.users["carol"] = &ContextUser{ID: 2, Username: "carol", Role: "admin"}
	store.storedGroups = []string{"admins"}

	cfg := ForwardAuthConfig{
		Enabled:             true,
		UserHeader:          "X-Forwarded-User",
		GroupsHeader:        "X-Forwarded-Groups",
		RequireGroupsHeader: true,
		DefaultRole:         "developer",
		GroupRoleMappings:   []GroupRoleMapping{{Group: "admins", Role: "admin"}},
	}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-User", "carol")
	// Deliberately NOT setting X-Forwarded-Groups - simulates proxy misconfiguration
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 in strict mode for missing groups header, got %d", w.Code)
	}
	if h.called {
		t.Fatal("inner handler must not be called when groups header is required but absent")
	}
	if len(store.reconcileCalls) != 0 {
		t.Fatalf("strict mode refusal must not trigger reconcile, got %d calls", len(store.reconcileCalls))
	}
	if UserFromContext(r.Context()) != nil {
		t.Fatal("no user should be attached to the request context")
	}
}

// TestForwardAuth_RequireGroupsHeader_PresentEmptyAllowed verifies that in strict
// mode, a request that sends the groups header with an EMPTY value is allowed
// through. A present-but-empty header is the authoritative signal that the user
// has no groups; it is not a missing header. The middleware reconciles with an
// empty group list (stripping any prior group-derived roles).
func TestForwardAuth_RequireGroupsHeader_PresentEmptyAllowed(t *testing.T) {
	store := newFakeStore()
	store.users["dave"] = &ContextUser{ID: 3, Username: "dave", Role: "admin"}
	store.storedGroups = []string{"admins"} // user currently has admin via group

	cfg := ForwardAuthConfig{
		Enabled:             true,
		UserHeader:          "X-Forwarded-User",
		GroupsHeader:        "X-Forwarded-Groups",
		RequireGroupsHeader: true,
		DefaultRole:         "developer",
		GroupRoleMappings:   []GroupRoleMapping{{Group: "admins", Role: "admin"}},
	}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-User", "dave")
	// Present but empty - authoritative "user has no groups"
	r.Header.Set("X-Forwarded-Groups", "")
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, r)

	if w.Code == http.StatusForbidden {
		t.Fatal("strict mode must allow a present-but-empty groups header, got 403")
	}
	if !h.called {
		t.Fatal("inner handler must be called when groups header is present (even if empty)")
	}
	// storedGroups has ["admins"] but incoming is [] - changed, so reconcile fires
	if len(store.reconcileCalls) != 1 {
		t.Fatalf("expected exactly 1 reconcile call for present-empty header, got %d", len(store.reconcileCalls))
	}
	if len(store.reconcileCalls[0].groups) != 0 {
		t.Errorf("reconcile must be called with empty groups, got %v", store.reconcileCalls[0].groups)
	}
}

// TestForwardAuth_RequireGroupsHeaderFalse_AbsentRevokes verifies that the
// default behavior (RequireGroupsHeader: false) is unchanged: a missing groups
// header causes reconcile to be called with an empty group list (revoking any
// group-derived roles), and the inner handler is still called.
//
// This test covers the same code path as
// TestForwardAuth_GroupsHeaderAbsent_RevokesStaleGroups, which predates the
// strict-mode feature. Both are kept because the older test uses "carol" and
// this one explicitly names RequireGroupsHeader: false to document the contract.
func TestForwardAuth_RequireGroupsHeaderFalse_AbsentRevokes(t *testing.T) {
	store := newFakeStore()
	store.users["carol"] = &ContextUser{ID: 2, Username: "carol", Role: "admin"}
	store.storedGroups = []string{"admins"}

	cfg := ForwardAuthConfig{
		Enabled:             true,
		UserHeader:          "X-Forwarded-User",
		GroupsHeader:        "X-Forwarded-Groups",
		RequireGroupsHeader: false, // explicit: preserve revoke behavior
		DefaultRole:         "developer",
		GroupRoleMappings:   []GroupRoleMapping{{Group: "admins", Role: "admin"}},
	}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-User", "carol")
	// No X-Forwarded-Groups - default mode should revoke, not refuse
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, r)

	if w.Code == http.StatusForbidden {
		t.Fatalf("default mode (RequireGroupsHeader: false) must not refuse absent groups header, got 403")
	}
	if !h.called {
		t.Fatal("inner handler must be called in default mode")
	}
	if len(store.reconcileCalls) != 1 {
		t.Fatalf("absent header in default mode must trigger reconcile with empty groups, got %d calls", len(store.reconcileCalls))
	}
	if len(store.reconcileCalls[0].groups) != 0 {
		t.Errorf("reconcile must be called with empty groups to revoke elevation, got %v", store.reconcileCalls[0].groups)
	}
}

// TestForwardAuth_RequireGroupsHeaderNoGroupsHeaderConfigured pins the contract
// that require_groups_header only takes effect when groups_header is configured.
// With an empty GroupsHeader the whole group block is skipped, so the strict-mode
// 403 never fires even when RequireGroupsHeader is true and no header is sent.
func TestForwardAuth_RequireGroupsHeaderNoGroupsHeaderConfigured(t *testing.T) {
	store := newFakeStore()
	store.users["erin"] = &ContextUser{ID: 4, Username: "erin", Role: "developer"}

	cfg := ForwardAuthConfig{
		Enabled:             true,
		UserHeader:          "X-Forwarded-User",
		GroupsHeader:        "", // groups not used; strict mode has nothing to enforce
		RequireGroupsHeader: true,
		DefaultRole:         "developer",
	}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-User", "erin")
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, r)

	if w.Code == http.StatusForbidden {
		t.Fatal("strict mode must not refuse when groups_header is unconfigured")
	}
	if !h.called {
		t.Fatal("inner handler must be called when groups_header is unconfigured")
	}
	if len(store.reconcileCalls) != 0 {
		t.Fatalf("no group reconcile should run when groups_header is unconfigured, got %d", len(store.reconcileCalls))
	}
}

// captureSlog replaces the default slog logger with one that writes to buf for
// the duration of the test, then restores the original on cleanup.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return &buf
}

// TestForwardAuth_UntrustedPeerWithUserHeader_LogsWarnOnce verifies that when
// forward auth is enabled, the request carries the configured user header, and
// the peer is NOT in trusted_proxies, a WARN log is emitted exactly once for
// that peer IP (subsequent requests from the same IP produce no further logs).
func TestForwardAuth_UntrustedPeerWithUserHeader_LogsWarnOnce(t *testing.T) {
	buf := captureSlog(t)

	store := newFakeStore()
	cfg := ForwardAuthConfig{Enabled: true, UserHeader: "X-Forwarded-User", DefaultRole: "developer"}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	sendRequest := func(addr string) {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = addr
		r.Header.Set("X-Forwarded-User", "mallory")
		mw.ServeHTTP(httptest.NewRecorder(), r)
	}

	// First request from the untrusted peer: must trigger a WARN.
	sendRequest("203.0.113.5:1234")
	if !strings.Contains(buf.String(), "forward_auth") {
		t.Fatalf("expected WARN log for untrusted peer with user header, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "203.0.113.5") {
		t.Fatalf("expected peer IP in WARN log, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "trusted_proxies") {
		t.Fatalf("expected trusted_proxies hint in WARN log, got: %s", buf.String())
	}

	// Request authenticates nothing: inner handler runs but no user is attached.
	if !h.called {
		t.Fatal("middleware must fall through to inner handler for untrusted peer")
	}
	if h.user != nil {
		t.Fatalf("untrusted peer must not authenticate user, got %+v", h.user)
	}

	// Second request from the same peer: must NOT produce a second log entry.
	buf.Reset()
	sendRequest("203.0.113.5:5678") // same IP, different port
	if strings.Contains(buf.String(), "forward_auth") {
		t.Fatalf("WARN must not repeat for same untrusted peer IP, got: %s", buf.String())
	}

	// A different untrusted peer gets its own first-time WARN.
	buf.Reset()
	sendRequest("198.51.100.7:9999")
	if !strings.Contains(buf.String(), "198.51.100.7") {
		t.Fatalf("expected WARN for new untrusted peer, got: %s", buf.String())
	}
}

// TestForwardAuth_TrustedPeerWithUserHeader_NoWarn verifies that a request from
// a trusted peer never triggers the misconfiguration warning, even when the
// user header is present.
func TestForwardAuth_TrustedPeerWithUserHeader_NoWarn(t *testing.T) {
	buf := captureSlog(t)

	store := newFakeStore()
	cfg := ForwardAuthConfig{Enabled: true, UserHeader: "X-Forwarded-User", DefaultRole: "developer"}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555" // trusted
	r.Header.Set("X-Forwarded-User", "alice")
	mw.ServeHTTP(httptest.NewRecorder(), r)

	if strings.Contains(buf.String(), "forward_auth") {
		t.Fatalf("trusted peer must not trigger warn log, got: %s", buf.String())
	}
}

// TestForwardAuth_UntrustedPeerNoUserHeader_NoWarn verifies that an untrusted
// peer without the user header does not trigger the misconfiguration warning
// (the header absence means there is nothing to warn about).
func TestForwardAuth_UntrustedPeerNoUserHeader_NoWarn(t *testing.T) {
	buf := captureSlog(t)

	store := newFakeStore()
	cfg := ForwardAuthConfig{Enabled: true, UserHeader: "X-Forwarded-User", DefaultRole: "developer"}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.9:1234" // untrusted, but no user header
	mw.ServeHTTP(httptest.NewRecorder(), r)

	if strings.Contains(buf.String(), "forward_auth") {
		t.Fatalf("untrusted peer without user header must not trigger warn log, got: %s", buf.String())
	}
}

// TestForwardAuth_NameHeader_CapturesDisplayName verifies that when a name
// header is configured and present, the middleware captures it as the user's
// display name and attaches it to the request context.
func TestForwardAuth_NameHeader_CapturesDisplayName(t *testing.T) {
	store := newFakeStore()
	cfg := ForwardAuthConfig{
		Enabled: true, UserHeader: "Remote-User", NameHeader: "Remote-Name", DefaultRole: "developer",
	}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("Remote-User", "alice")
	r.Header.Set("Remote-Name", "Alice Liddell")
	mw.ServeHTTP(httptest.NewRecorder(), r)

	if len(store.setNameCalls) != 1 || store.setNameCalls[0].name != "Alice Liddell" {
		t.Fatalf("expected one SetDisplayNameFromIdP call with \"Alice Liddell\", got %+v", store.setNameCalls)
	}
	if h.user == nil || h.user.DisplayName != "Alice Liddell" {
		t.Fatalf("expected display name on context, got %+v", h.user)
	}
}

// TestForwardAuth_NameHeader_UnchangedSkipsWrite verifies the equality guard:
// when the header matches the stored display name, no write is issued (this
// middleware runs on every request, so the unchanged case must stay read-only).
func TestForwardAuth_NameHeader_UnchangedSkipsWrite(t *testing.T) {
	store := newFakeStore()
	store.users["bob"] = &ContextUser{ID: 7, Username: "bob", Role: "developer", DisplayName: "Bob Builder"}
	cfg := ForwardAuthConfig{
		Enabled: true, UserHeader: "Remote-User", NameHeader: "Remote-Name", DefaultRole: "developer",
	}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("Remote-User", "bob")
	r.Header.Set("Remote-Name", "Bob Builder") // identical to stored
	mw.ServeHTTP(httptest.NewRecorder(), r)

	if len(store.setNameCalls) != 0 {
		t.Fatalf("unchanged name must not write, got calls %+v", store.setNameCalls)
	}
}

// TestForwardAuth_NameHeader_OffWhenUnconfiguredOrEmpty verifies no capture
// happens when the name header is not configured, and none happens when the
// header is configured but the request omits a value.
func TestForwardAuth_NameHeader_OffWhenUnconfiguredOrEmpty(t *testing.T) {
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	// Name header NOT configured: a Remote-Name header is ignored.
	store := newFakeStore()
	cfg := ForwardAuthConfig{Enabled: true, UserHeader: "Remote-User", DefaultRole: "developer"}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(&reachedHandler{})
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("Remote-User", "carol")
	r.Header.Set("Remote-Name", "Carol Danvers")
	mw.ServeHTTP(httptest.NewRecorder(), r)
	if len(store.setNameCalls) != 0 {
		t.Fatalf("unconfigured name header must not write, got %+v", store.setNameCalls)
	}

	// Configured but the request omits the value: no write.
	store2 := newFakeStore()
	cfg2 := ForwardAuthConfig{Enabled: true, UserHeader: "Remote-User", NameHeader: "Remote-Name", DefaultRole: "developer"}
	mw2 := ForwardAuthMiddleware(store2, cfg2, trusted)(&reachedHandler{})
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.RemoteAddr = "127.0.0.1:5555"
	r2.Header.Set("Remote-User", "dave")
	mw2.ServeHTTP(httptest.NewRecorder(), r2)
	if len(store2.setNameCalls) != 0 {
		t.Fatalf("missing name value must not write, got %+v", store2.setNameCalls)
	}
}

package auth

import (
	"errors"
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

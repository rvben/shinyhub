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
	users          map[string]*ContextUser
	created        []string // usernames passed to CreateForwardAuthUser
	rolePromotions []string // usernames promoted to admin
	getErr         error    // if set, GetForwardAuthUser returns this error
	promoteErr     error    // if set, PromoteToAdmin returns this error
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

func (f *fakeUserStore) PromoteToAdmin(userID int64) error {
	if f.promoteErr != nil {
		return f.promoteErr
	}
	for _, u := range f.users {
		if u.ID == userID {
			u.Role = "admin"
			f.rolePromotions = append(f.rolePromotions, u.Username)
			return nil
		}
	}
	return ErrUserNotFound
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

func TestForwardAuth_GroupsHeaderPromotesToAdmin(t *testing.T) {
	store := newFakeStore()
	cfg := ForwardAuthConfig{
		Enabled:      true,
		UserHeader:   "X-Forwarded-User",
		GroupsHeader: "X-Forwarded-Groups",
		AdminGroups:  []string{"shinyhub-admins"},
		DefaultRole:  "developer",
	}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	h := &reachedHandler{}
	mw := ForwardAuthMiddleware(store, cfg, trusted)(h)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-User", "bob")
	r.Header.Set("X-Forwarded-Groups", "engineers,shinyhub-admins,sre")
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, r)

	if h.user == nil || h.user.Role != "admin" {
		t.Fatalf("expected bob role=admin, got %+v", h.user)
	}
	if len(store.rolePromotions) != 1 || store.rolePromotions[0] != "bob" {
		t.Fatalf("expected bob promoted, got %v", store.rolePromotions)
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

func TestForwardAuth_PromoteError_Returns500(t *testing.T) {
	store := newFakeStore()
	store.promoteErr = errors.New("update failed")
	// Pre-populate bob so GetForwardAuthUser succeeds.
	store.users["bob"] = &ContextUser{ID: 1, Username: "bob", Role: "developer"}
	cfg := ForwardAuthConfig{
		Enabled:      true,
		UserHeader:   "X-Forwarded-User",
		GroupsHeader: "X-Forwarded-Groups",
		AdminGroups:  []string{"shinyhub-admins"},
		DefaultRole:  "developer",
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
		t.Fatal("inner handler must not be called on promote error")
	}
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}

package auth

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Count lookups as well as writes: malformed identity assertions must never
// reach the store, even when the user already exists.
type encodingStore struct {
	*fakeUserStore
	lookups int
}

func (s *encodingStore) GetForwardAuthUser(username string) (*ContextUser, error) {
	s.lookups++
	return s.fakeUserStore.GetForwardAuthUser(username)
}

func encodingConfig() ForwardAuthConfig {
	return ForwardAuthConfig{
		Enabled:      true,
		UserHeader:   "Remote-User",
		NameHeader:   "Remote-Name",
		EmailHeader:  "Remote-Email",
		GroupsHeader: "Remote-Groups",
		SecretHeader: "Proxy-Secret",
		SharedSecret: "proxy-secret",
		DefaultRole:  "viewer",
	}
}

func encodingRequest(cfg ForwardAuthConfig) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set(cfg.UserHeader, "ćirić")
	r.Header.Set(cfg.SecretHeader, cfg.SharedSecret)
	r.Header.Set(cfg.NameHeader, "Ana Ćirić")
	r.Header.Set(cfg.EmailHeader, "ćirić@example.com")
	r.Header.Set(cfg.GroupsHeader, "gg-équipe")
	return r
}

func TestForwardAuth_InvalidUTF8RejectsBeforeStore(t *testing.T) {
	for _, field := range []string{"user", "groups"} {
		for _, invalid := range []string{"\xe9", "\xc3", "\xc0\xaf", "\xed\xa0\x80"} {
			for _, existing := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%x/existing=%t", field, invalid, existing), func(t *testing.T) {
					cfg := encodingConfig()
					store := &encodingStore{fakeUserStore: newFakeStore()}
					if existing {
						username := "ćirić"
						if field == "user" {
							username = "bad" + invalid
						}
						store.users[username] = &ContextUser{ID: 1, Username: username, Role: "developer", DisplayName: "Stored name"}
					}
					h := &reachedHandler{}
					handler := ForwardAuthMiddleware(store, cfg, []*net.IPNet{mustCIDR(t, "127.0.0.0/8")})(h)
					r := encodingRequest(cfg)
					if field == "user" {
						r.Header.Set(cfg.UserHeader, "bad"+invalid)
					} else {
						// Validate every repeated field, not just Header.Get's first value.
						r.Header.Add(cfg.GroupsHeader, "bad"+invalid)
					}
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, r)
					if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), field+" header must contain valid UTF-8") {
						t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
					}
					if h.called || store.lookups != 0 || len(store.created) != 0 || len(store.reconcileCalls) != 0 || len(store.setNameCalls) != 0 {
						t.Fatalf("invalid assertion reached handler/store: handler=%v store=%+v", h.called, store)
					}
					for _, header := range []string{cfg.UserHeader, cfg.GroupsHeader, cfg.NameHeader, cfg.EmailHeader, cfg.SecretHeader} {
						if len(r.Header.Values(header)) != 0 {
							t.Errorf("ingress header %s was not stripped", header)
						}
					}
				})
			}
		}
	}
}

func TestForwardAuth_InvalidUTF8OptionalFields(t *testing.T) {
	for _, field := range []string{"name", "email", "both"} {
		t.Run(field, func(t *testing.T) {
			cfg := encodingConfig()
			store := newFakeStore()
			store.users["ćirić"] = &ContextUser{ID: 1, Username: "ćirić", Role: "developer", DisplayName: "Stored name", Email: "old@example.com"}
			store.storedGroups = []string{"gg-équipe"}
			h := &reachedHandler{}
			handler := ForwardAuthMiddleware(store, cfg, []*net.IPNet{mustCIDR(t, "127.0.0.0/8")})(h)
			r := encodingRequest(cfg)
			if field != "email" {
				r.Header.Set(cfg.NameHeader, "Jos\xe9")
			}
			if field != "name" {
				r.Header.Set(cfg.EmailHeader, "jos\xe9@example.com")
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusOK || !h.called {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if field != "email" && (h.user.DisplayName != "Stored name" || len(store.setNameCalls) != 0) {
				t.Fatalf("invalid name changed stored identity: %+v", h.user)
			}
			if field != "name" && h.user.Email != "" {
				t.Fatalf("invalid email forwarded or fell back to stored email: %q", h.user.Email)
			}
			if field == "name" && h.user.Email != "ćirić@example.com" {
				t.Fatalf("valid email lost: %q", h.user.Email)
			}
			if field == "email" && h.user.DisplayName != "Ana Ćirić" {
				t.Fatalf("valid name lost: %q", h.user.DisplayName)
			}
		})
	}
}

func TestForwardAuth_EncodingValidationRequiresTrustedCredential(t *testing.T) {
	for _, untrusted := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid credential", true: "untrusted peer"}[untrusted], func(t *testing.T) {
			cfg := encodingConfig()
			store := &encodingStore{fakeUserStore: newFakeStore()}
			h := &reachedHandler{}
			handler := ForwardAuthMiddleware(store, cfg, []*net.IPNet{mustCIDR(t, "127.0.0.0/8")})(h)
			r := encodingRequest(cfg)
			r.Header.Set(cfg.UserHeader, "bad\xe9")
			if untrusted {
				r.RemoteAddr = "192.0.2.1:1234"
			} else {
				r.Header.Set(cfg.SecretHeader, "wrong")
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			want := http.StatusServiceUnavailable
			if untrusted {
				want = http.StatusOK
			}
			if w.Code != want || store.lookups != 0 || h.user != nil {
				t.Fatalf("status=%d user=%+v lookups=%d", w.Code, h.user, store.lookups)
			}
			if h.called != untrusted {
				t.Fatalf("downstream called=%v", h.called)
			}
		})
	}
}

func TestForwardAuth_EncodingWarningsPerPeerAndField(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	cfg := encodingConfig()
	handler := ForwardAuthMiddleware(newFakeStore(), cfg, []*net.IPNet{mustCIDR(t, "127.0.0.0/8")})(&reachedHandler{})
	for _, peer := range []string{"127.0.0.1:1234", "127.0.0.1:5678", "127.0.0.2:1234"} {
		r := encodingRequest(cfg)
		r.RemoteAddr = peer
		r.Header.Set(cfg.NameHeader, "private-name\xe9")
		r.Header.Set(cfg.EmailHeader, "private-email\xe9")
		handler.ServeHTTP(httptest.NewRecorder(), r)
	}
	output := logs.String()
	if strings.Count(output, "identity header contains invalid UTF-8") != 4 {
		t.Fatalf("want one warning per peer and field: %s", output)
	}
	if strings.Contains(output, "private-") || strings.Contains(output, "ćirić") || strings.Contains(output, cfg.SharedSecret) {
		t.Fatalf("diagnostic leaked identity/credential: %s", output)
	}
}

func TestForwardAuth_RepeatedIdentityHeadersValidateEveryValue(t *testing.T) {
	for _, field := range []string{"user", "name", "email"} {
		t.Run(field, func(t *testing.T) {
			cfg := encodingConfig()
			store := &encodingStore{fakeUserStore: newFakeStore()}
			store.users["ćirić"] = &ContextUser{ID: 1, Username: "ćirić", DisplayName: "Stored name", Email: "old@example.com", Role: "developer"}
			store.storedGroups = []string{"gg-équipe"}
			h := &reachedHandler{}
			handler := ForwardAuthMiddleware(store, cfg, []*net.IPNet{mustCIDR(t, "127.0.0.0/8")})(h)
			r := encodingRequest(cfg)
			header := map[string]string{"user": cfg.UserHeader, "name": cfg.NameHeader, "email": cfg.EmailHeader}[field]
			r.Header.Add(header, "private\xe9")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if field == "user" {
				if w.Code != http.StatusBadRequest || h.called || store.lookups != 0 {
					t.Fatalf("invalid repeated user reached store: status=%d called=%v lookups=%d", w.Code, h.called, store.lookups)
				}
				return
			}
			if w.Code != http.StatusOK || h.user == nil {
				t.Fatalf("invalid optional assertion rejected request: status=%d", w.Code)
			}
			if field == "name" && (h.user.DisplayName != "Stored name" || len(store.setNameCalls) != 0) {
				t.Fatalf("repeated invalid name was not ignored: %+v", h.user)
			}
			if field == "email" && h.user.Email != "" {
				t.Fatalf("repeated invalid email forwarded/fell back: %q", h.user.Email)
			}
		})
	}
}

func TestForwardAuth_ValidRepeatedIdentityHeadersPreserveFirstValue(t *testing.T) {
	cfg := encodingConfig()
	store := newFakeStore()
	h := &reachedHandler{}
	handler := ForwardAuthMiddleware(store, cfg, []*net.IPNet{mustCIDR(t, "127.0.0.0/8")})(h)
	r := encodingRequest(cfg)
	r.Header.Add(cfg.UserHeader, "another-user")
	r.Header.Add(cfg.NameHeader, "Another name")
	r.Header.Add(cfg.EmailHeader, "another@example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || h.user == nil {
		t.Fatalf("valid repeated assertions: status=%d", w.Code)
	}
	if h.user.Username != "ćirić" || h.user.DisplayName != "Ana Ćirić" || h.user.Email != "ćirić@example.com" {
		t.Fatalf("first-value behavior changed: %+v", h.user)
	}
}

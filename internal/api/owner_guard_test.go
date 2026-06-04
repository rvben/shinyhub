package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
}

func TestOwnerGuard_NonOwnerBlocksMutations(t *testing.T) {
	s := &Server{isOwner: func() bool { return false }}
	h := s.ownerGuard(okHandler())

	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, "/api/apps/foo/deploy", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s mutation: code = %d, want 503", m, rec.Code)
		}
	}

	// Reads always pass.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/apps", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET read: code = %d, want 200", rec.Code)
	}

	for _, m := range []string{http.MethodHead, http.MethodOptions} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, "/api/apps", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s read: code = %d, want 200", m, rec.Code)
		}
	}

	// Auth endpoints pass even when mutating (login/logout during a handoff).
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("auth POST: code = %d, want 200", rec.Code)
	}
}

func TestOwnerGuard_OwnerAndUnwiredPass(t *testing.T) {
	cases := map[string]*Server{
		"owner":   {isOwner: func() bool { return true }},
		"unwired": {}, // nil predicate behaves as owner
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			h := s.ownerGuard(okHandler())
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/apps", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("code = %d, want 200", rec.Code)
			}
		})
	}
}

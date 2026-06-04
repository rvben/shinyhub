package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerMiddleware_YieldsToExistingContextUser(t *testing.T) {
	// No Authorization header, no cookie - but a user is already in context
	// (as forward-auth would set it). Bearer must pass through, not 401.
	reached := false
	var got *ContextUser
	h := BearerMiddleware("secret", nil, nil, nil)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			reached = true
			got = UserFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

	r := httptest.NewRequest("GET", "/", nil)
	pre := &ContextUser{ID: 7, Username: "alice", Role: "developer"}
	r = r.WithContext(context.WithValue(r.Context(), userContextKey, pre))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if !reached {
		t.Fatalf("expected pass-through, got status %d", w.Code)
	}
	if got == nil || got.Username != "alice" {
		t.Fatalf("expected alice preserved in context, got %+v", got)
	}
}

func TestBearerMiddleware_NoUserNoCredential_Still401(t *testing.T) {
	h := BearerMiddleware("secret", nil, nil, nil)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when no user and no credential, got %d", w.Code)
	}
}

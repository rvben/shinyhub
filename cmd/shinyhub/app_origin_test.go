package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
)

type fakeAppLaunchStore struct {
	createdHash string
	createdUser int64
	createdSlug string
	user        *auth.ContextUser
	consumed    bool
}

func (f *fakeAppLaunchStore) CreateAppLaunchCode(hash string, userID int64, slug string) error {
	f.createdHash, f.createdUser, f.createdSlug = hash, userID, slug
	return nil
}

func (f *fakeAppLaunchStore) ConsumeAppLaunchCode(hash, slug string) (*auth.ContextUser, error) {
	if f.consumed || hash != f.createdHash || slug != f.createdSlug {
		return nil, errors.New("invalid launch")
	}
	f.consumed = true
	return f.user, nil
}

func TestAppOriginLaunchExchangesOneTimeCodeForHostOnlySession(t *testing.T) {
	appOrigin, _ := url.Parse("https://apps.example.com")
	user := &auth.ContextUser{ID: 42, Username: "alice", Role: "developer"}
	store := &fakeAppLaunchStore{user: user}
	control := appOriginRedirectHandler(store, appOrigin)
	proxyHits := 0
	app := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits++
		w.WriteHeader(http.StatusNoContent)
	})
	handler := appOriginDispatch(appOrigin, nil, store, "01234567890123456789012345678901", control, app)

	controlReq := httptest.NewRequest(http.MethodGet, "https://hub.example.com/app/sales/?tab=one", nil)
	controlReq = controlReq.WithContext(auth.WithUser(controlReq.Context(), user))
	controlRec := httptest.NewRecorder()
	handler.ServeHTTP(controlRec, controlReq)
	if controlRec.Code != http.StatusSeeOther {
		t.Fatalf("control status = %d, want 303", controlRec.Code)
	}
	location, err := url.Parse(controlRec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	rawCode := location.Query().Get(appLaunchQueryParam)
	if location.Host != appOrigin.Host || rawCode == "" || location.Query().Get("tab") != "one" {
		t.Fatalf("unexpected launch redirect: %s", location)
	}
	sum := sha256.Sum256([]byte(rawCode))
	if store.createdHash != hex.EncodeToString(sum[:]) || store.createdUser != user.ID || store.createdSlug != "sales" {
		t.Fatalf("launch was not correctly hashed and bound: %#v", store)
	}

	appReq := httptest.NewRequest(http.MethodGet, location.String(), nil)
	appRec := httptest.NewRecorder()
	handler.ServeHTTP(appRec, appReq)
	if appRec.Code != http.StatusSeeOther {
		t.Fatalf("exchange status = %d, want 303", appRec.Code)
	}
	if got := appRec.Header().Get("Location"); got != "/app/sales/?tab=one" {
		t.Fatalf("clean redirect = %q", got)
	}
	var session *http.Cookie
	for _, cookie := range appRec.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			session = cookie
		}
	}
	if session == nil || !session.HttpOnly || !session.Secure || session.Domain != "" {
		t.Fatalf("unexpected app session cookie: %#v", session)
	}
	if proxyHits != 0 {
		t.Fatalf("launch exchange reached proxy %d times", proxyHits)
	}

	replayRec := httptest.NewRecorder()
	handler.ServeHTTP(replayRec, appReq)
	if replayRec.Code != http.StatusUnauthorized {
		t.Fatalf("replay status = %d, want 401", replayRec.Code)
	}
}

func TestAppOriginBoundaryHidesControlPlaneRoutes(t *testing.T) {
	appOrigin, _ := url.Parse("https://apps.example.com")
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := appOriginBoundary(next, appOrigin, nil)

	for _, path := range []string{"/api/users", "/static/app.js", "/internal/fargate-bundle/x", "/internal/runtime-bundle/x", "/"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://apps.example.com"+path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, rec.Code)
		}
	}
	for _, path := range []string{"/app/sales/", "/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://apps.example.com"+path, nil))
		if rec.Code != http.StatusNoContent {
			t.Errorf("%s status = %d, want 204", path, rec.Code)
		}
	}
}

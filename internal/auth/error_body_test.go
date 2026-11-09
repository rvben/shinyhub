package auth

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// assertJSONError requires a JSON error body carrying want, the shape every
// /api client parses.
func assertJSONError(t *testing.T, rec *httptest.ResponseRecorder, status int, want string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, rec.Body.String())
	}
	if body["error"] != want {
		t.Fatalf("error = %q, want %q", body["error"], want)
	}
}

func TestBearerRejectionIsJSON(t *testing.T) {
	h := BearerMiddleware("test-secret", nil, nil, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler reached without credentials")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/apps", nil))
	assertJSONError(t, rec, http.StatusUnauthorized, "unauthorized")
}

func TestCSRFRejectionIsJSON(t *testing.T) {
	h := CSRFMiddleware(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler reached without a CSRF token")
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/apps", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "any"})
	req.Header.Set("Referer", "https://hub.example.com/apps")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assertJSONError(t, rec, http.StatusForbidden, "csrf: missing token")
}

// Forward auth fronts every path, so a rejection answers an API caller with
// JSON and a page load with readable text.
func TestForwardAuthRejectionMatchesTheCaller(t *testing.T) {
	cfg := encodingConfig()
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}
	h := ForwardAuthMiddleware(&encodingStore{fakeUserStore: newFakeStore()}, cfg, trusted)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler reached with an invalid identity header")
	}))
	const want = "forward auth: user header must contain valid UTF-8"

	api := encodingRequest(cfg)
	api.Header.Set(cfg.UserHeader, "bad\xe9")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, api)
	assertJSONError(t, rec, http.StatusBadRequest, want)

	page := encodingRequest(cfg)
	page.Header.Set(cfg.UserHeader, "bad\xe9")
	page.Header.Set("Sec-Fetch-Mode", "navigate")
	page.Header.Set("Accept", "text/html")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, page)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("page status = %d, want 400", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("page Content-Type = %q, want text/plain", ct)
	}
	if got := rec.Body.String(); got != want+"\n" {
		t.Fatalf("page body = %q, want %q", got, want+"\n")
	}
}

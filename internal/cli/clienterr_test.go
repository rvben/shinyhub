package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnwrapServerError(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		fallback string
		want     string
	}{
		{"standard envelope", `{"error":"app not found"}`, "request failed", "app not found"},
		{"envelope with surrounding whitespace", "  {\"error\":\"bad slug\"}\n", "request failed", "bad slug"},
		{"empty error field falls through to body", `{"error":""}`, "request failed", `{"error":""}`},
		{"non-json body trimmed", "  boom  ", "request failed", "boom"},
		{"empty body uses fallback", "", "request failed", "request failed"},
		{"whitespace-only body uses fallback", "   \n", "request failed", "request failed"},
		{"json without error field returns trimmed body", `{"message":"x"}`, "request failed", `{"message":"x"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := unwrapServerError([]byte(tc.body), tc.fallback)
			if got != tc.want {
				t.Errorf("unwrapServerError(%q, %q) = %q, want %q", tc.body, tc.fallback, got, tc.want)
			}
		})
	}
}

// A structurally-valid JWT: three non-empty dot-separated segments with an
// "eyJ" header. Only the shape matters here, not the signature.
const testJWT = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln"

func TestHTTPError_ExpiredJWTSurfacesLoginHint(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized"}
	err := httpError(testJWT, "list apps", resp, []byte(`{"error":"unauthorized"}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "session expired") {
		t.Errorf("expected message to mention session expiry, got %q", msg)
	}
	if !strings.Contains(msg, "shinyhub login") {
		t.Errorf("expected message to suggest `shinyhub login`, got %q", msg)
	}
	if strings.Contains(msg, "—") || strings.Contains(msg, "–") {
		t.Errorf("message must not contain em/en dashes, got %q", msg)
	}
}

func TestHTTPError_401WithAPIKeyIsNotSessionExpired(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized"}
	err := httpError("shk_abc123", "rollback", resp, []byte(`{"error":"invalid token"}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if strings.Contains(msg, "session expired") {
		t.Errorf("API-key 401 must not claim session expiry, got %q", msg)
	}
	if !strings.Contains(msg, "invalid token") {
		t.Errorf("expected server envelope to be surfaced, got %q", msg)
	}
}

func TestHTTPError_401WithOpaqueDeployTokenIsNotSessionExpired(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized"}
	err := httpError("0123456789abcdef0123456789abcdef", "stop", resp, []byte(`{"error":"unauthorized"}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if strings.Contains(err.Error(), "session expired") {
		t.Errorf("opaque deploy-token 401 must not claim session expiry, got %q", err.Error())
	}
}

func TestHTTPError_Non401WithJWTUsesEnvelope(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found"}
	err := httpError(testJWT, "show app", resp, []byte(`{"error":"app not found"}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if strings.Contains(msg, "session expired") {
		t.Errorf("404 must not claim session expiry, got %q", msg)
	}
	if !strings.Contains(msg, "app not found") {
		t.Errorf("expected server envelope to be surfaced, got %q", msg)
	}
	if !strings.Contains(msg, "404") {
		t.Errorf("expected status to be surfaced, got %q", msg)
	}
	if !strings.Contains(msg, "show app") {
		t.Errorf("expected operation label to be surfaced, got %q", msg)
	}
}

// writeJWTConfig points HOME at a temp dir holding a credentials file whose
// token is a JWT, so loadConfig resolves a JWT-shaped credential exactly as a
// `shinyhub login` session would.
func writeJWTConfig(t *testing.T, host string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHINYHUB_HOST", "")
	t.Setenv("SHINYHUB_TOKEN", "")
	cfgDir := filepath.Join(home, ".config", "shinyhub")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(cfgDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(f).Encode(cliConfig{Host: host, Token: testJWT}); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// End-to-end: a real command (apps list) run with a stored JWT against a server
// that 401s must tell the developer their session lapsed, not just relay the
// server's bare "unauthorized".
func TestAppsList_ExpiredSessionSurfacesLoginHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	t.Cleanup(srv.Close)
	writeJWTConfig(t, srv.URL)

	_, err := execCLI(t, "apps", "list")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "session expired") || !strings.Contains(msg, "shinyhub login") {
		t.Errorf("expected session-expired login hint, got %q", msg)
	}
}

// TestHTTPError_ReturnsTypedStatusError verifies httpError returns a
// *httpStatusError carrying the status code, so the root classifier can map
// status to kind without string parsing.
func TestHTTPError_ReturnsTypedStatusError(t *testing.T) {
	resp := &http.Response{StatusCode: 404, Status: "404 Not Found"}
	err := httpError("shk_sometoken", "show app", resp, []byte(`{"error":"no such app"}`))
	var hse *httpStatusError
	if !errors.As(err, &hse) {
		t.Fatalf("httpError did not return *httpStatusError: %T %v", err, err)
	}
	if hse.Status != 404 {
		t.Errorf("Status = %d, want 404", hse.Status)
	}
	if got, want := err.Error(), "show app (404 Not Found): no such app"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestHTTPError_SessionExpiredKeepsTypedStatus verifies the JWT-expiry special
// case still carries the 401 status for classification.
func TestHTTPError_SessionExpiredKeepsTypedStatus(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln"
	resp := &http.Response{StatusCode: 401, Status: "401 Unauthorized"}
	err := httpError(jwt, "list apps", resp, nil)
	var hse *httpStatusError
	if !errors.As(err, &hse) {
		t.Fatalf("session-expired error is not *httpStatusError: %T", err)
	}
	if hse.Status != 401 {
		t.Errorf("Status = %d, want 401", hse.Status)
	}
	if got := err.Error(); got != "session expired - run `shinyhub login` to sign in again" {
		t.Errorf("Error() = %q", got)
	}
}

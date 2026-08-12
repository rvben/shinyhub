package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCredentialLifecycleWarningBoundary(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		expiry *time.Time
		want   string
	}{
		{"never", nil, "non_expiring"},
		{"healthy", timePointer(now.Add(credentialExpiryWarningWindow + time.Second)), "healthy"},
		{"exactly fourteen days", timePointer(now.Add(credentialExpiryWarningWindow)), "expiring"},
		{"expired", timePointer(now.Add(-time.Second)), "expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := credentialLifecycleAt(&remoteCredential{Type: "api_key", ExpiresAt: tc.expiry}, now)
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q", got.Status, tc.want)
			}
		})
	}
	if got := credentialLifecycleAt(nil, now).Status; got != "unknown" {
		t.Fatalf("legacy server status = %q, want unknown", got)
	}
}

func timePointer(t time.Time) *time.Time { return &t }

func TestDoctorWarnsBeforeCredentialExpires(t *testing.T) {
	isolatedCredentials(t)
	expires := time.Now().UTC().Add(13 * 24 * time.Hour)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"1.7.0","runtimes":{"python":true}}`)
		case "/api/auth/me":
			_, _ = fmt.Fprintf(w, `{"user":{"username":"alice","role":"developer"},"can_create_apps":true,"credential":{"type":"api_key","id":7,"name":"cli-laptop","created_at":"2026-07-01T00:00:00Z","last_used_at":"2026-08-01T00:00:00Z","expires_at":%q}}`, expires.Format(time.RFC3339))
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	if err := saveConfig(&cliConfig{Host: srv.URL, Token: "shk_doctor"}); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := execCLISplit(t, "doctor", "--remote", "--output", "json")
	if err != nil {
		t.Fatalf("doctor: %v (stderr=%q)", err, stderr)
	}
	report := decodeDoctorReport(t, stdout)
	check := doctorCheckNamed(t, report, "credential-lifecycle")
	if check.Status != "warn" || !strings.Contains(check.Fix, "connect --refresh") {
		t.Fatalf("lifecycle check = %+v", check)
	}
	if check.Credential == nil || check.Credential.Name != "cli-laptop" || check.Credential.CreatedAt == nil || check.Credential.LastUsedAt == nil || check.Credential.Status != "expiring" {
		t.Fatalf("structured credential lifecycle = %+v", check.Credential)
	}
	if report.Status != "ready" || report.Summary.Warned == 0 {
		t.Fatalf("warning should stay deploy-ready: %+v", report)
	}
}

func TestConnectRefreshRotatesAtomicallyAndRevokesPreviousKey(t *testing.T) {
	isolatedCredentials(t)
	var newAuthorization string
	var revoked bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"1.7.0","capabilities":{"cli_connect":true},"runtimes":{"python":true}}`)
		case "/api/auth/cli-connect/status":
			_, _ = io.WriteString(w, `{"status":"approved"}`)
		case "/api/auth/me":
			authorization := r.Header.Get("Authorization")
			if authorization == "Token shk_old" {
				_, _ = io.WriteString(w, `{"user":{"username":"alice","role":"developer"},"can_create_apps":true,"credential":{"type":"api_key","id":41,"name":"cli-old"}}`)
				return
			}
			if authorization == "Token shk_from_environment" {
				t.Fatal("--refresh must ignore SHINYHUB_TOKEN")
			}
			newAuthorization = authorization
			_, _ = io.WriteString(w, `{"user":{"username":"alice","role":"developer"},"can_create_apps":true,"credential":{"type":"api_key","id":42,"name":"cli-new"}}`)
		case "/api/tokens/41":
			if r.Method != http.MethodDelete || r.Header.Get("Authorization") != newAuthorization {
				t.Fatalf("revoke request used the wrong method or credential")
			}
			revoked = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	st := &credentialStore{CurrentHost: srv.URL, Hosts: map[string]hostCredential{
		srv.URL:                 {Name: "prod", Token: "shk_old", User: "alice"},
		"https://other.example": {Name: "other", Token: "shk_other", User: "bob"},
	}}
	if err := saveStore(st); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHINYHUB_TOKEN", "shk_from_environment")

	cmd, out, _ := connectTestCommand()
	if err := runConnect(cmd, nil, &connectFlags{refresh: true, noBrowser: true, timeout: time.Second}); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
		t.Fatal(err)
	}
	if result["status"] != "refreshed" || result["previous_credential_revoked"] != true || !revoked {
		t.Fatalf("refresh result = %+v, revoked=%v", result, revoked)
	}
	after, err := loadStore()
	if err != nil {
		t.Fatal(err)
	}
	if after.Hosts[srv.URL].Token == "shk_old" || after.Hosts[srv.URL].Name != "prod" {
		t.Fatalf("target was not rotated while preserving alias: %+v", after.Hosts[srv.URL])
	}
	if after.Hosts["https://other.example"].Token != "shk_other" {
		t.Fatalf("refresh changed another host: %+v", after.Hosts)
	}
}

func TestConnectRefreshFailureLeavesCredentialsByteExact(t *testing.T) {
	isolatedCredentials(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"1.7.0","capabilities":{"cli_connect":true}}`)
		case "/api/auth/me":
			_, _ = io.WriteString(w, `{"user":{"username":"alice","role":"developer"},"credential":{"type":"api_key","id":9}}`)
		case "/api/auth/cli-connect/status":
			http.Error(w, "pairing unavailable", http.StatusServiceUnavailable)
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	if err := saveNamedConfig(&cliConfig{Host: srv.URL, Token: "shk_working"}, "prod", "alice"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(configPath())
	if err != nil {
		t.Fatal(err)
	}

	cmd, _, _ := connectTestCommand()
	err = runConnect(cmd, nil, &connectFlags{refresh: true, noBrowser: true, timeout: time.Second})
	if err == nil {
		t.Fatal("refresh should fail when pairing fails")
	}
	after, readErr := os.ReadFile(configPath())
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(before) {
		t.Fatalf("failed refresh changed credentials\nbefore=%s\nafter=%s", before, after)
	}
}

func TestConnectRefreshRejectsDifferentBrowserIdentity(t *testing.T) {
	isolatedCredentials(t)
	var cleanedNewCredential bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"1.7.0","capabilities":{"cli_connect":true}}`)
		case "/api/auth/cli-connect/status":
			_, _ = io.WriteString(w, `{"status":"approved"}`)
		case "/api/auth/me":
			if r.Header.Get("Authorization") == "Token shk_alice" {
				_, _ = io.WriteString(w, `{"user":{"username":"alice","role":"developer"},"can_create_apps":true,"credential":{"type":"api_key","id":10}}`)
				return
			}
			_, _ = io.WriteString(w, `{"user":{"username":"bob","role":"developer"},"can_create_apps":true,"credential":{"type":"api_key","id":11}}`)
		case "/api/tokens/11":
			cleanedNewCredential = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	if err := saveNamedConfig(&cliConfig{Host: srv.URL, Token: "shk_alice"}, "prod", "alice"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(configPath())
	if err != nil {
		t.Fatal(err)
	}

	cmd, _, _ := connectTestCommand()
	err = runConnect(cmd, nil, &connectFlags{refresh: true, noBrowser: true, timeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "browser approved bob") {
		t.Fatalf("identity mismatch error = %v", err)
	}
	after, readErr := os.ReadFile(configPath())
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(before) || !cleanedNewCredential {
		t.Fatalf("identity mismatch changed local credentials or orphaned the new key: unchanged=%v cleaned=%v", string(after) == string(before), cleanedNewCredential)
	}
}

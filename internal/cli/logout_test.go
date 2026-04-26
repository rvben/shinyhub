package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestLogout_RemovesConfigAndCallsServer verifies the happy path: with a
// valid config file, logout POSTs to /api/auth/logout (so the server can
// revoke the JWT) and then deletes the credentials file.
func TestLogout_RemovesConfigAndCallsServer(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/auth/logout" {
			atomic.AddInt32(&calls, 1)
			if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Token ") && !strings.HasPrefix(got, "Bearer ") {
				t.Errorf("expected auth header on logout, got %q", got)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHINYHUB_HOST", "")
	t.Setenv("SHINYHUB_TOKEN", "")
	t.Setenv("SHINYHUB_CONFIG", "")
	configPathOverride = ""

	if err := saveConfig(&cliConfig{Host: srv.URL, Token: "shk_logout"}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	path := configPath()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config file should exist before logout: %v", err)
	}

	if err := runLogout(logoutCmd, nil); err != nil {
		t.Fatalf("runLogout: %v", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("expected 1 call to /api/auth/logout, got %d", calls)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("config file should be removed after logout, stat err: %v", err)
	}
}

// TestLogout_IdempotentWhenNotLoggedIn verifies logout exits cleanly when
// no credentials are stored — no panic, no error, no network call.
func TestLogout_IdempotentWhenNotLoggedIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHINYHUB_HOST", "")
	t.Setenv("SHINYHUB_TOKEN", "")
	t.Setenv("SHINYHUB_CONFIG", "")
	configPathOverride = ""

	if err := runLogout(logoutCmd, nil); err != nil {
		t.Fatalf("runLogout when not logged in should be a no-op, got %v", err)
	}
}

// TestLogout_RemovesConfigEvenWhenServerUnreachable ensures that a network
// failure on the revoke call still removes the local credentials — the user
// asked to log out, so the local file must go regardless.
func TestLogout_RemovesConfigEvenWhenServerUnreachable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHINYHUB_HOST", "")
	t.Setenv("SHINYHUB_TOKEN", "")
	t.Setenv("SHINYHUB_CONFIG", "")
	configPathOverride = ""

	// Point at a port that is not listening so the revoke call fails.
	if err := saveConfig(&cliConfig{Host: "http://127.0.0.1:1", Token: "shk_dead"}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	path := configPath()

	if err := runLogout(logoutCmd, nil); err != nil {
		t.Fatalf("runLogout should succeed even when server is unreachable: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("config file should still be removed when server is unreachable, stat err: %v", err)
	}
}

// TestLogout_WarnsWhenEnvCredsRemain verifies that when SHINYHUB_TOKEN is
// set in the environment, runLogout surfaces a clear warning telling the
// user to unset it. Without that warning the CLI prints "Logged out" while
// the next command silently re-authenticates from env — particularly bad
// for API keys (shk_) which have no server-side revocation endpoint.
func TestLogout_WarnsWhenEnvCredsRemain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_envonly")
	t.Setenv("SHINYHUB_CONFIG", "")
	configPathOverride = ""

	var stdout, stderr bytes.Buffer
	logoutCmd.SetOut(&stdout)
	logoutCmd.SetErr(&stderr)
	t.Cleanup(func() {
		logoutCmd.SetOut(nil)
		logoutCmd.SetErr(nil)
	})

	if err := runLogout(logoutCmd, nil); err != nil {
		t.Fatalf("runLogout: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "SHINYHUB_TOKEN") {
		t.Fatalf("expected logout output to warn about SHINYHUB_TOKEN env var; got:\n%s", out)
	}
	if !strings.Contains(out, "unset SHINYHUB_HOST SHINYHUB_TOKEN") {
		t.Fatalf("expected logout output to suggest `unset SHINYHUB_HOST SHINYHUB_TOKEN` (both vars are set); got:\n%s", out)
	}
}

// TestLogout_WarnsWhenOnlyTokenEnvSet verifies the env-warning still fires
// when SHINYHUB_TOKEN is set without SHINYHUB_HOST (host comes from the
// config file). The warning must mention only SHINYHUB_TOKEN — telling the
// user to unset a variable they never set is confusing.
func TestLogout_WarnsWhenOnlyTokenEnvSet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHINYHUB_HOST", "")
	t.Setenv("SHINYHUB_TOKEN", "shk_envtoken")
	t.Setenv("SHINYHUB_CONFIG", "")
	configPathOverride = ""

	if err := saveConfig(&cliConfig{Host: srv.URL, Token: "shk_filetoken"}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	var stdout bytes.Buffer
	logoutCmd.SetOut(&stdout)
	t.Cleanup(func() { logoutCmd.SetOut(nil) })

	if err := runLogout(logoutCmd, nil); err != nil {
		t.Fatalf("runLogout: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "unset SHINYHUB_TOKEN") {
		t.Fatalf("expected logout output to suggest `unset SHINYHUB_TOKEN`; got:\n%s", out)
	}
	if strings.Contains(out, "SHINYHUB_HOST") {
		t.Fatalf("expected warning to mention only SHINYHUB_TOKEN (HOST not set); got:\n%s", out)
	}
}

// TestLogout_NoEnvWarningWhenEnvUnset verifies the warning is suppressed in
// the common case where credentials live only in the config file. Adding
// noise to the file-only happy path would degrade UX.
func TestLogout_NoEnvWarningWhenEnvUnset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHINYHUB_HOST", "")
	t.Setenv("SHINYHUB_TOKEN", "")
	t.Setenv("SHINYHUB_CONFIG", "")
	configPathOverride = ""

	if err := saveConfig(&cliConfig{Host: srv.URL, Token: "shk_clean"}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	var stdout bytes.Buffer
	logoutCmd.SetOut(&stdout)
	t.Cleanup(func() { logoutCmd.SetOut(nil) })

	if err := runLogout(logoutCmd, nil); err != nil {
		t.Fatalf("runLogout: %v", err)
	}

	out := stdout.String()
	if strings.Contains(out, "SHINYHUB_TOKEN") || strings.Contains(out, "SHINYHUB_HOST") {
		t.Fatalf("expected no env-var warning when env is unset; got:\n%s", out)
	}
}

// TestLogout_HonorsConfigOverride verifies the --config override is used
// to locate the credentials file to delete (not the default path).
func TestLogout_HonorsConfigOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHINYHUB_HOST", "")
	t.Setenv("SHINYHUB_TOKEN", "")
	t.Setenv("SHINYHUB_CONFIG", "")

	custom := filepath.Join(t.TempDir(), "alt.json")
	configPathOverride = custom
	t.Cleanup(func() { configPathOverride = "" })

	if err := saveConfig(&cliConfig{Host: srv.URL, Token: "shk_alt"}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if _, err := os.Stat(custom); err != nil {
		t.Fatalf("custom config should exist: %v", err)
	}

	if err := runLogout(logoutCmd, nil); err != nil {
		t.Fatalf("runLogout: %v", err)
	}
	if _, err := os.Stat(custom); !os.IsNotExist(err) {
		t.Errorf("custom config should be removed, stat err: %v", err)
	}
	// The default path must not have been touched.
	if _, err := os.Stat(filepath.Join(home, ".config", "shinyhub", "config.json")); !os.IsNotExist(err) {
		t.Errorf("default path should remain absent, stat err: %v", err)
	}
}

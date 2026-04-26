package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestVerifyToken_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/me" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	t.Cleanup(srv.Close)

	err := verifyToken(srv.URL, "bad_token")
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should contain status code, got: %v", err)
	}
}

func TestVerifyToken_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	t.Cleanup(srv.Close)

	if err := verifyToken(srv.URL, "shk_forbidden"); err == nil {
		t.Fatal("expected error for 403, got nil")
	}
}

func TestVerifyToken_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/me" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user":{"id":1,"username":"admin","role":"admin"}}`))
	}))
	t.Cleanup(srv.Close)

	if err := verifyToken(srv.URL, "shk_good"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestRunLogin_PromptsForPasswordWhenStdinIsTTY guards the new-user handoff
// snippet. The snippet shown in the admin's "New user" modal is just
// `shinyhub login --host X --username Y` — without a `--password` flag the
// previous behaviour POSTed an empty password and surfaced "login failed:
// 401 Unauthorized" with no hint about how to recover. Interactive sessions
// must now prompt for the missing password (via term.ReadPassword in
// production; stubbed here) so the snippet is runnable as-is.
func TestRunLogin_PromptsForPasswordWhenStdinIsTTY(t *testing.T) {
	// Capture the JSON body the server receives so we can assert that the
	// prompted password was forwarded — not the empty string from the flag.
	var seen struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/login" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &seen)
		_, _ = w.Write([]byte(`{"token":"shk_via_prompt"}`))
	}))
	t.Cleanup(srv.Close)

	// Redirect the config write to a temp dir so the test doesn't clobber
	// the developer's real ~/.shinyhub/config.yaml.
	t.Setenv("HOME", t.TempDir())

	// Stub the tty + ReadPassword seams.
	origIsTTY, origReadPw := isStdinTTY, readPassword
	t.Cleanup(func() { isStdinTTY, readPassword = origIsTTY, origReadPw })
	isStdinTTY = func() bool { return true }
	readPassword = func() (string, error) { return "secret123", nil }

	// Reset and configure the package-level loginFlags the way the CLI
	// would after parsing `shinyhub login --host X --username alice`.
	loginFlags.host = srv.URL
	loginFlags.username = "alice"
	loginFlags.password = ""
	loginFlags.token = ""
	t.Cleanup(func() {
		loginFlags.host = ""
		loginFlags.username = ""
		loginFlags.password = ""
		loginFlags.token = ""
	})

	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	if err := runLogin(cmd, nil); err != nil {
		t.Fatalf("runLogin: %v", err)
	}
	if seen.Username != "alice" {
		t.Errorf("server saw username=%q, want %q", seen.Username, "alice")
	}
	if seen.Password != "secret123" {
		t.Errorf("server saw password=%q, want the prompted value", seen.Password)
	}
}

// TestRunLogin_NoPromptWhenStdinIsPipe guards the script-friendly path. When
// stdin is not a tty (e.g. the user pipes credentials from a secrets manager),
// the login command must not block on a prompt — it should pass the empty
// flags through unchanged so the server's existing 401 surfaces with the same
// behaviour as before.
func TestRunLogin_NoPromptWhenStdinIsPipe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	t.Cleanup(srv.Close)

	t.Setenv("HOME", t.TempDir())

	origIsTTY, origReadPw := isStdinTTY, readPassword
	t.Cleanup(func() { isStdinTTY, readPassword = origIsTTY, origReadPw })
	isStdinTTY = func() bool { return false }
	readPassword = func() (string, error) {
		t.Fatal("readPassword must not be called when stdin is a pipe; would block forever in CI")
		return "", nil
	}

	loginFlags.host = srv.URL
	loginFlags.username = "alice"
	loginFlags.password = ""
	loginFlags.token = ""
	t.Cleanup(func() {
		loginFlags.host = ""
		loginFlags.username = ""
		loginFlags.password = ""
		loginFlags.token = ""
	})

	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	err := runLogin(cmd, nil)
	if err == nil {
		t.Fatal("expected 401 error from server, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should surface server status, got: %v", err)
	}
}

// TestRunLogin_PromptsRouteThroughCobraStreams pins the cobra-streams
// contract for the interactive login path. Previously the prompts went
// to cmd.OutOrStdout() and the line input came from os.Stdin directly,
// which meant:
//
//  1. `shinyhub login | cmd` would interleave "Username: " and "Password: "
//     into the consumer's stdin — invisible-to-the-user breakage.
//  2. Tests had to fake a real tty to drive the username path because the
//     reader was hard-coded to os.Stdin instead of cmd.InOrStdin().
//
// The fix routes prompts through cmd.ErrOrStderr() and reads usernames
// from cmd.InOrStdin(). This test drives both prompts via test streams
// and asserts: prompts land on stderr, success message lands on stdout,
// neither cross-contaminates, and the values reach the server.
func TestRunLogin_PromptsRouteThroughCobraStreams(t *testing.T) {
	var seen struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &seen)
		_, _ = w.Write([]byte(`{"token":"shk_streams"}`))
	}))
	t.Cleanup(srv.Close)

	t.Setenv("HOME", t.TempDir())

	origIsTTY, origReadPw := isStdinTTY, readPassword
	t.Cleanup(func() { isStdinTTY, readPassword = origIsTTY, origReadPw })
	isStdinTTY = func() bool { return true }
	readPassword = func() (string, error) { return "hunter2", nil }

	// Both flags empty so both prompts fire.
	loginFlags.host = srv.URL
	loginFlags.username = ""
	loginFlags.password = ""
	loginFlags.token = ""
	t.Cleanup(func() {
		loginFlags.host = ""
		loginFlags.username = ""
		loginFlags.password = ""
		loginFlags.token = ""
	})

	var stdout, stderr bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetIn(strings.NewReader("alice\n"))

	if err := runLogin(cmd, nil); err != nil {
		t.Fatalf("runLogin: %v", err)
	}

	if seen.Username != "alice" {
		t.Errorf("server saw username=%q, want %q (read from cmd.InOrStdin)", seen.Username, "alice")
	}
	if seen.Password != "hunter2" {
		t.Errorf("server saw password=%q, want %q (read via readPassword seam)", seen.Password, "hunter2")
	}

	// Prompts must go to stderr so a `shinyhub login | jq ...` pipeline
	// keeps stdout clean for the success message.
	errOut := stderr.String()
	if !strings.Contains(errOut, "Username: ") {
		t.Errorf("stderr should contain `Username: ` prompt; got %q", errOut)
	}
	if !strings.Contains(errOut, "Password: ") {
		t.Errorf("stderr should contain `Password: ` prompt; got %q", errOut)
	}

	// Stdout must NOT carry the prompts and MUST carry the success message.
	out := stdout.String()
	if strings.Contains(out, "Username: ") || strings.Contains(out, "Password: ") {
		t.Errorf("stdout must not contain prompt text (would corrupt downstream pipes); got %q", out)
	}
	if !strings.Contains(out, "Logged in.") {
		t.Errorf("stdout should contain the `Logged in.` success message; got %q", out)
	}
}

func TestVerifyToken_ServerDown(t *testing.T) {
	// Use a port unlikely to be listening.
	err := verifyToken("http://127.0.0.1:1", "shk_test")
	if err == nil {
		t.Fatal("expected error when server is unreachable, got nil")
	}
	if !strings.Contains(err.Error(), "connect to server") {
		t.Errorf("error should mention connection failure, got: %v", err)
	}
}

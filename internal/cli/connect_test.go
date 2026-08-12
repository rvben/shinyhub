package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
)

func connectTestCommand() (*cobra.Command, *strings.Builder, *strings.Builder) {
	cmd := &cobra.Command{}
	out, errOut := &strings.Builder{}, &strings.Builder{}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetIn(strings.NewReader(""))
	return cmd, out, errOut
}

func TestConnectTargetHostAcceptsGlobalHostSelectors(t *testing.T) {
	isolatedCredentials(t)
	st := &credentialStore{
		CurrentHost: "https://current.example.com",
		Hosts: map[string]hostCredential{
			"https://prod.example.com": {Name: "prod"},
		},
	}
	cmd, _, _ := connectTestCommand()

	hostFlagOverride = "prod"
	got, err := connectTargetHost(cmd, nil, st)
	if err != nil || got != "https://prod.example.com" {
		t.Fatalf("--host alias resolved to %q, err=%v", got, err)
	}

	hostFlagOverride = "HTTPS://New.Example.COM/"
	got, err = connectTargetHost(cmd, nil, st)
	if err != nil || got != "https://new.example.com" {
		t.Fatalf("--host URL resolved to %q, err=%v", got, err)
	}
}

func TestConnectTargetHostRejectsTwoExplicitTargets(t *testing.T) {
	isolatedCredentials(t)
	hostFlagOverride = "https://flag.example.com"
	cmd, _, _ := connectTestCommand()

	_, err := connectTargetHost(cmd, []string{"https://argument.example.com"}, &credentialStore{})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined with --host") {
		t.Fatalf("error = %v, want conflicting target guidance", err)
	}
}

func TestConnect_WithTokenVerifiesAndSavesRemoteContext(t *testing.T) {
	isolatedCredentials(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"1.4.0","capabilities":{"content_digest":true},"runtimes":{"python":true,"r":false}}`)
		case "/api/auth/me":
			if r.Header.Get("Authorization") != "Token shk_remote" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"user":{"username":"alice","role":"developer"},"can_create_apps":true}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	cmd, out, progress := connectTestCommand()
	if err := runConnect(cmd, []string{srv.URL}, &connectFlags{token: "shk_remote", name: "prod", timeout: defaultConnectTimeout}); err != nil {
		t.Fatalf("runConnect: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
		t.Fatalf("decode output %q: %v", out.String(), err)
	}
	if result["status"] != "connected" || result["user"] != "alice" || result["can_create_apps"] != true {
		t.Errorf("result = %+v", result)
	}
	if !strings.Contains(progress.String(), "ShinyHub 1.4.0 is ready · python") {
		t.Errorf("progress should report verified version/runtime, got %q", progress.String())
	}
	st, err := loadStore()
	if err != nil || st.CurrentHost != srv.URL || st.Hosts[srv.URL].Token != "shk_remote" {
		t.Fatalf("saved store = %+v, err=%v", st, err)
	}
}

func TestConnect_BrowserFlowKeepsRawCredentialOutOfURL(t *testing.T) {
	isolatedCredentials(t)
	var approved atomic.Bool
	var seenAuthorization string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"1.4.0","capabilities":{"content_digest":true,"cli_connect":true},"runtimes":{"python":true,"r":true}}`)
		case "/api/auth/cli-connect/status":
			status := "pending"
			if approved.Load() {
				status = "approved"
			}
			_, _ = io.WriteString(w, `{"status":"`+status+`"}`)
		case "/api/auth/me":
			seenAuthorization = r.Header.Get("Authorization")
			if !approved.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"user":{"username":"sso-user","role":"developer"},"can_create_apps":true}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	origTTY, origOpen := isStdinTTY, openBrowserURL
	t.Cleanup(func() { isStdinTTY, openBrowserURL = origTTY, origOpen })
	isStdinTTY = func() bool { return true }
	var opened string
	openBrowserURL = func(target string) error {
		opened = target
		approved.Store(true)
		return nil
	}

	cmd, _, _ := connectTestCommand()
	if err := runConnect(cmd, []string{srv.URL}, &connectFlags{timeout: defaultConnectTimeout}); err != nil {
		t.Fatalf("runConnect: %v", err)
	}
	u, err := url.Parse(opened)
	if err != nil || u.Path != "/tokens" {
		t.Fatalf("authorization URL = %q, err=%v", opened, err)
	}
	if u.Query().Get("connect_hash") == "" || u.Query().Get("connect_code") == "" {
		t.Errorf("authorization URL lacks pairing context: %s", opened)
	}
	raw := strings.TrimPrefix(seenAuthorization, "Token ")
	if raw == "" || !strings.HasPrefix(raw, "shk_") {
		t.Fatalf("CLI did not poll with generated credential: %q", seenAuthorization)
	}
	if strings.Contains(opened, raw) {
		t.Fatal("raw CLI credential leaked into browser URL")
	}
}

func TestConnect_NonInteractiveRequiresCredential(t *testing.T) {
	isolatedCredentials(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"version":"1.4.0","capabilities":{"content_digest":true}}`)
	}))
	t.Cleanup(srv.Close)
	origTTY := isStdinTTY
	t.Cleanup(func() { isStdinTTY = origTTY })
	isStdinTTY = func() bool { return false }

	cmd, _, _ := connectTestCommand()
	err := runConnect(cmd, []string{srv.URL}, &connectFlags{timeout: defaultConnectTimeout})
	if err == nil || !strings.Contains(err.Error(), "requires a terminal") {
		t.Fatalf("error = %v, want non-interactive credential guidance", err)
	}
	if hint := hintOf(err); !strings.Contains(hint, "--no-browser") {
		t.Errorf("hint = %q, want copy/paste pairing guidance", hint)
	}
}

func TestConnect_NoBrowserSupportsRedirectedOutput(t *testing.T) {
	isolatedCredentials(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"1.4.0","capabilities":{"cli_connect":true},"runtimes":{"python":true}}`)
		case "/api/auth/cli-connect/status":
			_, _ = io.WriteString(w, `{"status":"approved"}`)
		case "/api/auth/me":
			_, _ = io.WriteString(w, `{"user":{"username":"ssh-user","role":"developer"},"can_create_apps":true}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	origTTY, origOpen := isStdinTTY, openBrowserURL
	t.Cleanup(func() { isStdinTTY, openBrowserURL = origTTY, origOpen })
	isStdinTTY = func() bool { return false }
	openBrowserURL = func(string) error {
		t.Fatal("--no-browser must not try to open a local browser")
		return nil
	}

	cmd, out, progress := connectTestCommand()
	if err := runConnect(cmd, []string{srv.URL}, &connectFlags{noBrowser: true, timeout: defaultConnectTimeout}); err != nil {
		t.Fatalf("runConnect: %v", err)
	}
	if !strings.Contains(progress.String(), srv.URL+"/tokens?") || !strings.Contains(progress.String(), "Browser approval received") {
		t.Errorf("copy/paste pairing progress = %q", progress.String())
	}
	if !strings.Contains(out.String(), `"status":"connected"`) {
		t.Errorf("connect output = %q", out.String())
	}
}

func TestConnect_OlderServerExplainsTokenFallback(t *testing.T) {
	isolatedCredentials(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"version":"0.11.4","capabilities":{"content_digest":true}}`)
	}))
	t.Cleanup(srv.Close)
	origTTY := isStdinTTY
	t.Cleanup(func() { isStdinTTY = origTTY })
	isStdinTTY = func() bool { return true }

	cmd, _, _ := connectTestCommand()
	err := runConnect(cmd, []string{srv.URL}, &connectFlags{timeout: defaultConnectTimeout})
	if err == nil || !strings.Contains(err.Error(), "does not support browser CLI authorization") {
		t.Fatalf("error = %v", err)
	}
	if hint := hintOf(err); !strings.Contains(hint, "--token-file") || !strings.Contains(hint, "upgrade") {
		t.Errorf("fallback hint = %q", hint)
	}
}

func TestFirstDeployOffersConnectionOnlyInInteractiveTableMode(t *testing.T) {
	isolatedCredentials(t)
	origTTY := isStdinTTY
	origOutput := outputFlagValue
	origResolved := resolvedFormat
	t.Cleanup(func() { isStdinTTY = origTTY; outputFlagValue = origOutput; resolvedFormat = origResolved })
	isStdinTTY = func() bool { return true }
	outputFlagValue = "table"
	resolvedFormat = ""

	cmd, _, progress := connectTestCommand()
	cmd.SetIn(strings.NewReader("n\n"))
	connected, err := offerConnectForFirstDeploy(cmd)
	if err != nil || connected {
		t.Fatalf("declined offer = connected %v, err %v", connected, err)
	}
	if !strings.Contains(progress.String(), "No remote ShinyHub is connected yet") ||
		!strings.Contains(progress.String(), "Connect one now? [Y/n]") {
		t.Errorf("first deploy prompt = %q", progress.String())
	}

	isStdinTTY = func() bool { return false }
	cmd, _, progress = connectTestCommand()
	connected, err = offerConnectForFirstDeploy(cmd)
	if err != nil || connected || progress.Len() != 0 {
		t.Fatalf("non-interactive offer must be silent: connected=%v err=%v output=%q", connected, err, progress.String())
	}
}

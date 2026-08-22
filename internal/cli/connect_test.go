package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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

func TestDeployPermissionSummaryReportsAppScope(t *testing.T) {
	identity := remoteIdentity{
		CanCreateApps: true,
		AppScope:      []string{"energy", "observability"},
	}
	if got := deployPermissionSummary(identity); got != "Yes — restricted to energy, observability" {
		t.Fatalf("permission summary = %q", got)
	}
	if got := deployNextStep(identity); got != "Next: deploy one of the allowlisted apps: energy, observability." {
		t.Fatalf("next step = %q", got)
	}
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
	t.Setenv("SHINYHUB_TOKEN", "shk_inherited")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"1.4.0","capabilities":{"content_digest":true},"runtimes":{"python":true,"r":false}}`)
		case "/api/auth/me":
			if r.Header.Get("Authorization") != "Token shk_remote" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"user":{"username":"alice","role":"developer"},"can_create_apps":true,"app_scope":["energy","observability"]}`)
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
	if got := result["app_scope"].([]any); len(got) != 2 || got[0] != "energy" || got[1] != "observability" {
		t.Errorf("app_scope = %#v", got)
	}
	if !strings.Contains(progress.String(), "ShinyHub 1.4.0 is ready · python") {
		t.Errorf("progress should report verified version/runtime, got %q", progress.String())
	}
	st, err := loadStore()
	if err != nil || st.CurrentHost != srv.URL || st.Hosts[srv.URL].Token != "shk_remote" {
		t.Fatalf("saved store = %+v, err=%v", st, err)
	}
}

func TestConnect_EnvironmentCredentialWinsOverSavedCredential(t *testing.T) {
	isolatedCredentials(t)
	t.Setenv("SHINYHUB_TOKEN", "shk_environment")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"1.7.0","runtimes":{"python":true}}`)
		case "/api/auth/me":
			if r.Header.Get("Authorization") != "Token shk_environment" {
				t.Fatalf("identity used %q, want environment credential", r.Header.Get("Authorization"))
			}
			_, _ = io.WriteString(w, `{"user":{"username":"ci","role":"developer"},"can_create_apps":true}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	if err := saveNamedConfig(&cliConfig{Host: srv.URL, Token: "shk_saved"}, "prod", "alice"); err != nil {
		t.Fatal(err)
	}

	cmd, out, _ := connectTestCommand()
	if err := runConnect(cmd, []string{srv.URL}, &connectFlags{timeout: defaultConnectTimeout}); err != nil {
		t.Fatalf("runConnect: %v", err)
	}
	if !strings.Contains(out.String(), `"status":"connected"`) {
		t.Fatalf("output = %s", out.String())
	}
	after, err := loadStore()
	if err != nil {
		t.Fatal(err)
	}
	if after.Hosts[srv.URL].Token != "shk_environment" {
		t.Fatalf("saved token = %q, want environment credential", after.Hosts[srv.URL].Token)
	}
}

func TestConnect_ExplicitUsernameWinsOverEnvironmentCredential(t *testing.T) {
	isolatedCredentials(t)
	t.Setenv("SHINYHUB_TOKEN", "shk_inherited")
	const session = "eyJhbGciOiJub25lIn0.e30.signature"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"1.7.0","runtimes":{"python":true}}`)
		case "/api/auth/login":
			_, _ = io.WriteString(w, `{"token":"`+session+`"}`)
		case "/api/auth/me":
			if r.Header.Get("Authorization") != "Bearer "+session {
				t.Fatalf("identity used %q, want explicit username session", r.Header.Get("Authorization"))
			}
			_, _ = io.WriteString(w, `{"user":{"username":"alice","role":"developer"},"can_create_apps":true}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	cmd, _, _ := connectTestCommand()
	if err := runConnect(cmd, []string{srv.URL}, &connectFlags{username: "alice", password: "secret", timeout: defaultConnectTimeout}); err != nil {
		t.Fatalf("runConnect: %v", err)
	}
	after, err := loadStore()
	if err != nil {
		t.Fatal(err)
	}
	if after.Hosts[srv.URL].Token != session {
		t.Fatalf("saved token = %q, want explicit username session", after.Hosts[srv.URL].Token)
	}
}

func TestConnect_ValidSavedCredentialIsCurrentWithoutRotation(t *testing.T) {
	path := isolatedCredentials(t)
	var browserCalls, pairingCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"1.7.0","capabilities":{"cli_connect":true},"runtimes":{"python":true}}`)
		case "/api/auth/me":
			if r.Header.Get("Authorization") != "Token shk_saved" {
				t.Fatalf("identity used %q, want the saved credential", r.Header.Get("Authorization"))
			}
			_, _ = io.WriteString(w, `{"user":{"username":"alice","role":"developer"},"can_create_apps":true,"credential":{"type":"api_key","id":77,"name":"cli-laptop"}}`)
		case "/api/auth/cli-connect/status":
			pairingCalls++
			_, _ = io.WriteString(w, `{"status":"approved"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	if err := saveStore(&credentialStore{CurrentHost: srv.URL, Hosts: map[string]hostCredential{
		srv.URL: {Name: "prod", Token: "shk_saved", User: "alice", SavedAt: "2026-08-01T12:00:00Z"},
	}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	origTTY, origOpen := isStdinTTY, openBrowserURL
	t.Cleanup(func() { isStdinTTY, openBrowserURL = origTTY, origOpen })
	isStdinTTY = func() bool { return true }
	openBrowserURL = func(string) error {
		browserCalls++
		return nil
	}

	cmd, out, _ := connectTestCommand()
	if err := runConnect(cmd, []string{srv.URL}, &connectFlags{name: "prod", timeout: defaultConnectTimeout}); err != nil {
		t.Fatalf("runConnect: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
		t.Fatalf("decode output %q: %v", out.String(), err)
	}
	credential, _ := result["credential"].(map[string]any)
	if result["status"] != "current" || credential["id"] != float64(77) {
		t.Fatalf("result = %+v", result)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("current connect rewrote credentials\nbefore=%s\nafter=%s", before, after)
	}
	if browserCalls != 0 || pairingCalls != 0 {
		t.Fatalf("current connect used browser=%d pairing=%d", browserCalls, pairingCalls)
	}
}

func TestConnect_CurrentTableOutputIsDistinct(t *testing.T) {
	isolatedCredentials(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"1.7.0","runtimes":{"python":true}}`)
		case "/api/auth/me":
			_, _ = io.WriteString(w, `{"user":{"username":"alice","role":"developer"},"can_create_apps":true}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	if err := saveNamedConfig(&cliConfig{Host: srv.URL, Token: "shk_saved"}, "prod", "alice"); err != nil {
		t.Fatal(err)
	}
	oldOutput, oldResolved := outputFlagValue, resolvedFormat
	t.Cleanup(func() { outputFlagValue, resolvedFormat = oldOutput, oldResolved })
	outputFlagValue, resolvedFormat = "table", ""

	cmd, out, _ := connectTestCommand()
	if err := runConnect(cmd, []string{srv.URL}, &connectFlags{timeout: defaultConnectTimeout}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Already connected to") {
		t.Fatalf("table output = %q, want current connection wording", out.String())
	}
}

func TestConnect_CurrentCredentialStillAppliesSelectionAndName(t *testing.T) {
	isolatedCredentials(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"1.7.0","runtimes":{"python":true}}`)
		case "/api/auth/me":
			_, _ = io.WriteString(w, `{"user":{"username":"alice","role":"developer"},"can_create_apps":true}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	const previousHost = "https://previous.example.com"
	if err := saveStore(&credentialStore{CurrentHost: previousHost, Hosts: map[string]hostCredential{
		previousHost: {Name: "previous", Token: "shk_previous", User: "bob", SavedAt: "2026-07-01T00:00:00Z"},
		srv.URL:      {Name: "old-name", Token: "shk_saved", User: "alice", SavedAt: "2026-08-01T12:00:00Z"},
	}}); err != nil {
		t.Fatal(err)
	}

	cmd, out, _ := connectTestCommand()
	if err := runConnect(cmd, []string{srv.URL}, &connectFlags{name: "prod", timeout: defaultConnectTimeout}); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
		t.Fatal(err)
	}
	after, err := loadStore()
	if err != nil {
		t.Fatal(err)
	}
	got := after.Hosts[srv.URL]
	if result["status"] != "current" || result["switched_from"] != previousHost || after.CurrentHost != srv.URL {
		t.Fatalf("result=%+v current=%q", result, after.CurrentHost)
	}
	if got.Name != "prod" || got.Token != "shk_saved" || got.SavedAt != "2026-08-01T12:00:00Z" {
		t.Fatalf("target credential = %+v", got)
	}
	if after.Hosts[previousHost].Token != "shk_previous" {
		t.Fatalf("other host changed: %+v", after.Hosts[previousHost])
	}
}

func TestConnect_RejectedSavedCredentialReauthorizes(t *testing.T) {
	isolatedCredentials(t)
	var browserCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"1.7.0","capabilities":{"cli_connect":true}}`)
		case "/api/auth/me":
			if r.Header.Get("Authorization") == "Token shk_rejected" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"user":{"username":"alice","role":"developer"},"can_create_apps":true}`)
		case "/api/auth/cli-connect/status":
			_, _ = io.WriteString(w, `{"status":"approved"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	if err := saveNamedConfig(&cliConfig{Host: srv.URL, Token: "shk_rejected"}, "prod", "alice"); err != nil {
		t.Fatal(err)
	}
	origTTY, origOpen := isStdinTTY, openBrowserURL
	t.Cleanup(func() { isStdinTTY, openBrowserURL = origTTY, origOpen })
	isStdinTTY = func() bool { return true }
	openBrowserURL = func(string) error { browserCalls++; return nil }

	cmd, out, _ := connectTestCommand()
	if err := runConnect(cmd, []string{srv.URL}, &connectFlags{timeout: defaultConnectTimeout}); err != nil {
		t.Fatalf("runConnect: %v", err)
	}
	if browserCalls != 1 || !strings.Contains(out.String(), `"status":"connected"`) {
		t.Fatalf("browser calls=%d output=%s", browserCalls, out.String())
	}
	after, err := loadStore()
	if err != nil {
		t.Fatal(err)
	}
	if after.Hosts[srv.URL].Token == "shk_rejected" {
		t.Fatal("rejected saved credential was not replaced")
	}
}

func TestConnect_SavedCredentialServerFailureDoesNotRotate(t *testing.T) {
	path := isolatedCredentials(t)
	var browserCalls, pairingCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"1.7.0","capabilities":{"cli_connect":true}}`)
		case "/api/auth/me":
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		case "/api/auth/cli-connect/status":
			pairingCalls++
			http.Error(w, "must not pair", http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	if err := saveNamedConfig(&cliConfig{Host: srv.URL, Token: "shk_saved"}, "prod", "alice"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	origTTY, origOpen := isStdinTTY, openBrowserURL
	t.Cleanup(func() { isStdinTTY, openBrowserURL = origTTY, origOpen })
	isStdinTTY = func() bool { return true }
	openBrowserURL = func(string) error { browserCalls++; return errors.New("must not open browser") }

	cmd, _, _ := connectTestCommand()
	err = runConnect(cmd, []string{srv.URL}, &connectFlags{timeout: defaultConnectTimeout})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("error = %v, want saved-credential server failure", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(before) || browserCalls != 0 || pairingCalls != 0 {
		t.Fatalf("transient failure changed state: same=%v browser=%d pairing=%d", string(after) == string(before), browserCalls, pairingCalls)
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
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
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
	if hint := hintOf(err); !strings.Contains(hint, "--username") || !strings.Contains(hint, "--password") {
		t.Errorf("hint = %q, want complete username/password guidance", hint)
	}
	if requests != 0 {
		t.Fatalf("non-interactive missing credentials made %d request(s), want immediate local failure", requests)
	}
}

func TestConnect_NonInteractiveUsernameRequiresPasswordBeforeProbing(t *testing.T) {
	isolatedCredentials(t)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	origTTY := isStdinTTY
	t.Cleanup(func() { isStdinTTY = origTTY })
	isStdinTTY = func() bool { return false }

	cmd, _, _ := connectTestCommand()
	err := runConnect(cmd, []string{srv.URL}, &connectFlags{username: "alice", timeout: defaultConnectTimeout})
	if err == nil || !strings.Contains(hintOf(err), "--password") {
		t.Fatalf("error = %v hint=%q, want non-interactive password guidance", err, hintOf(err))
	}
	if requests != 0 {
		t.Fatalf("missing password made %d request(s), want immediate local failure", requests)
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

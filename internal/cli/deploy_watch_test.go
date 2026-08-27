package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deployevent"
	slugpkg "github.com/rvben/shinyhub/internal/slug"
	"github.com/spf13/cobra"
)

func TestDeployWatchRequiresExplicitHost(t *testing.T) {
	t.Setenv("SHINYHUB_HOST", "")
	hostFlagOverride = ""
	stdout, _, err := execCLISplit(t, "deploy", deployTestBundleDir(t), "--watch", "--watch-delay", "100ms", "-o", "table")
	if err == nil || !strings.Contains(err.Error(), "explicit remote host") {
		t.Fatalf("error = %v, stdout=%q", err, stdout)
	}
}

func TestDeployWatchRejectsGitAndSingleDocumentJSON(t *testing.T) {
	t.Setenv("SHINYHUB_HOST", "https://dev.example.test")
	_, _, err := execCLISplit(t, "deploy", "--git", "https://example.test/app.git", "--watch", "-o", "table")
	if err == nil || !strings.Contains(err.Error(), "does not support --git") {
		t.Fatalf("--git error = %v", err)
	}

	dir := deployTestBundleDir(t)
	_, _, err = execCLISplit(t, "deploy", dir, "--watch", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "cannot emit a single JSON document") {
		t.Fatalf("json error = %v", err)
	}
}

func TestDeployWatchOnlyFlagsRequireWatch(t *testing.T) {
	for _, flag := range []string{"--watch-delay=1s", "--allow-repeated-hooks=false", "--create", "--ephemeral", "--ttl=8h"} {
		_, _, err := execCLISplit(t, "deploy", deployTestBundleDir(t), flag)
		if err == nil || !strings.Contains(err.Error(), "require --watch") {
			t.Errorf("%s error = %v", flag, err)
		}
	}
}

func TestDeployWatchProtectsRepeatedHooks(t *testing.T) {
	dir := deployTestBundleDir(t)
	manifest := "[[hook]]\non = \"post-deploy\"\ncommand = [\"python\", \"prepare.py\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "shinyhub.toml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	err := validateRepeatedWatchHooks(dir, false)
	if err == nil || !strings.Contains(err.Error(), "after every deployable change") || !strings.Contains(hintOf(err), "--allow-repeated-hooks") {
		t.Fatalf("hook guard error = %v", err)
	}
	if err := validateRepeatedWatchHooks(dir, true); err != nil {
		t.Fatalf("explicit hook opt-in rejected: %v", err)
	}
}

func TestGeneratedDevelopmentSlugIsValidAndBounded(t *testing.T) {
	longDir := filepath.Join(t.TempDir(), strings.Repeat("long-project-name-", 10))
	got, err := generatedDevelopmentSlug([]string{longDir})
	if err != nil {
		t.Fatal(err)
	}
	if !slugpkg.Valid(got) {
		t.Fatalf("generated slug %q is invalid", got)
	}
	if len(got) > slugpkg.MaxLen {
		t.Fatalf("generated slug length = %d, want <= %d", len(got), slugpkg.MaxLen)
	}
	if !strings.Contains(got, "-dev-") {
		t.Fatalf("generated slug %q does not identify a development target", got)
	}
}

func TestWatchErrorIsFatalOnlyWhenRetryCannotHelp(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "deploy validation", err: &deployHTTPError{statusCode: http.StatusBadRequest}, want: true},
		{name: "deploy server error", err: &deployHTTPError{statusCode: http.StatusBadGateway}, want: false},
		{name: "auth", err: &httpStatusError{Status: http.StatusUnauthorized, msg: "unauthorized"}, want: true},
		{name: "deleted app", err: &httpStatusError{Status: http.StatusNotFound, msg: "not found"}, want: true},
		{name: "rate limited", err: &httpStatusError{Status: http.StatusTooManyRequests, msg: "slow down"}, want: false},
		{name: "local validation", err: validationErr("invalid", "fix it"), want: true},
		{name: "transport", err: errors.New("connection reset"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := watchErrorIsFatal(tc.err); got != tc.want {
				t.Fatalf("watchErrorIsFatal(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestWaitForDebouncedWatchChangeCoalescesBurst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes := make(chan struct{}, 4)
	changes <- struct{}{}
	go func() {
		time.Sleep(15 * time.Millisecond)
		changes <- struct{}{}
		time.Sleep(15 * time.Millisecond)
		changes <- struct{}{}
	}()
	started := time.Now()
	if !waitForDebouncedWatchChange(ctx, changes, 50*time.Millisecond) {
		t.Fatal("debounce returned false")
	}
	if elapsed := time.Since(started); elapsed < 75*time.Millisecond {
		t.Fatalf("debounce ended after %s; want 50ms after the final change", elapsed)
	}
}

func TestDeployWatchNDJSONLifecycleIsStructured(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	source := &deploymentSource{Dir: "/work/app", Slug: "demo"}
	cfg := &cliConfig{Host: "https://dev.example.test"}
	f := &deployFlags{watchDelay: 750 * time.Millisecond}
	if err := writeWatchStarted(cmd, formatNDJSON, source, cfg, f); err != nil {
		t.Fatal(err)
	}
	if err := writeWatchDeploying(cmd, formatNDJSON); err != nil {
		t.Fatal(err)
	}
	if err := writeWatchUnchanged(cmd, formatNDJSON); err != nil {
		t.Fatal(err)
	}
	if err := writeWatchFailure(cmd, formatNDJSON, errors.New("candidate failed")); err != nil {
		t.Fatal(err)
	}
	writeWatchStopped(cmd, formatNDJSON)

	var events []deployevent.Event
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var event deployevent.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if len(events) != 5 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Phase != "watch" || events[0].Status != deployevent.StatusStarted || events[3].Type != deployevent.TypeError || events[4].Status != deployevent.StatusCompleted {
		t.Fatalf("unexpected watch event lifecycle: %+v", events)
	}
}

func TestDeployWatchFailurePreservesActionableHint(t *testing.T) {
	cmd := &cobra.Command{}
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := validationErr("unsafe repeated hook", "pass --allow-repeated-hooks after review")
	if writeErr := writeWatchFailure(cmd, formatTable, err); writeErr != nil {
		t.Fatal(writeErr)
	}
	if !strings.Contains(stderr.String(), "Hint: pass --allow-repeated-hooks after review") {
		t.Fatalf("table failure omitted hint:\n%s", stderr.String())
	}

	stdout.Reset()
	if writeErr := writeWatchFailure(cmd, formatNDJSON, err); writeErr != nil {
		t.Fatal(writeErr)
	}
	var event deployevent.Event
	if jsonErr := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &event); jsonErr != nil {
		t.Fatal(jsonErr)
	}
	if !strings.Contains(event.Message, "Hint: pass --allow-repeated-hooks after review") {
		t.Fatalf("NDJSON failure omitted hint: %+v", event)
	}
}

func TestPrepareWatchTargetRequiresExplicitCreation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	cfg := &cliConfig{Host: srv.URL, Token: "token"}
	f := &deployFlags{}
	err := prepareWatchTarget(cfg, "missing", f)
	if err == nil || !strings.Contains(err.Error(), "does not exist") || !strings.Contains(hintOf(err), "--watch --create") {
		t.Fatalf("default missing target error = %v", err)
	}
}

func TestPrepareWatchTargetCreatesPersistentOrEphemeralAppExplicitly(t *testing.T) {
	for _, tc := range []struct {
		name       string
		flags      deployFlags
		wantTarget string
		wantAccess string
		wantExpiry bool
	}{
		{name: "persistent", flags: deployFlags{create: true, developmentSessionID: "0123456789abcdef0123456789abcdef", visibility: "shared"}, wantTarget: "created", wantAccess: "shared"},
		{name: "ephemeral", flags: deployFlags{ephemeral: true, ttl: time.Hour, developmentSessionID: "abcdef0123456789abcdef0123456789"}, wantTarget: "ephemeral", wantAccess: "private", wantExpiry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var created map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
					t.Fatal(err)
				}
				w.WriteHeader(http.StatusCreated)
			}))
			defer srv.Close()
			cfg := &cliConfig{Host: srv.URL, Token: "token"}
			f := tc.flags
			if err := prepareWatchTarget(cfg, "demo", &f); err != nil {
				t.Fatal(err)
			}
			if created["development_target"] != tc.wantTarget || created["access"] != tc.wantAccess {
				t.Fatalf("create body = %+v", created)
			}
			_, hasExpiry := created["expires_at"]
			if hasExpiry != tc.wantExpiry {
				t.Fatalf("expires_at present = %v, body=%+v", hasExpiry, created)
			}
		})
	}
}

func TestDeployWatchDeploysInitialAndCoalescedLatestChange(t *testing.T) {
	previousPoll := watchPollInterval
	watchPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { watchPollInterval = previousPoll })

	var deploys atomic.Int32
	var watchHeaders atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo":
			_, _ = w.Write([]byte(`{"app":{"slug":"demo","status":"running","access":"private","deploy_count":2,"current_version":"v2"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/demo/deploy":
			deploys.Add(1)
			if r.Header.Get("X-Shinyhub-Deploy-Channel") == "watch" &&
				len(r.Header.Get("X-Shinyhub-Development-Session")) == 32 &&
				r.Header.Get("X-Shinyhub-Development-Target") == "existing" {
				watchHeaders.Add(1)
			}
			_, _ = w.Write([]byte(`{"slug":"demo","status":"running","access":"private","deploy_count":3,"current_version":"v3"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	writeTestCLIConfig(t, srv.URL)

	dir := deployTestBundleDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := &cobra.Command{Use: "shinyhub", SilenceErrors: true}
	AddCommandsTo(root)
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	for _, sub := range allSubcommands(root) {
		sub.SetOut(&stdout)
		sub.SetErr(&stderr)
	}
	root.SetContext(ctx)
	root.SetArgs([]string{"deploy", dir, "--slug", "demo", "--watch", "--watch-delay", "100ms", "--host", srv.URL, "-o", "table"})
	done := make(chan error, 1)
	go func() { done <- root.Execute() }()

	waitForAtomicCount(t, &deploys, 1)
	// A metadata-only touch wakes the filesystem watcher, but the canonical
	// content digest is unchanged and must prevent a remote mutation.
	appPath := filepath.Join(dir, "app.py")
	touched := time.Now().Add(time.Second)
	if err := os.Chtimes(appPath, touched, touched); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	if got := deploys.Load(); got != 1 {
		t.Fatalf("metadata-only source change caused %d total deploys, want 1", got)
	}
	for _, body := range []string{"# edit one\n", "# edit two\n", "# final edit\n"} {
		if err := os.WriteFile(appPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitForAtomicCount(t, &deploys, 2)
	time.Sleep(180 * time.Millisecond)
	if got := deploys.Load(); got != 2 {
		t.Fatalf("deploys after one save burst = %d, want 2 total", got)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watch returned error: %v\nstderr=%s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not stop after context cancellation")
	}
	if got := watchHeaders.Load(); got != 2 {
		t.Fatalf("watch-attributed deploys = %d, want 2", got)
	}
	if !strings.Contains(stderr.String(), "Remote development") || !strings.Contains(stderr.String(), srv.URL+"/app/demo/") {
		t.Fatalf("watch banner did not keep the target visible:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Stopped remote development watch") {
		t.Fatalf("missing clean stop message:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "No deployable changes") {
		t.Fatalf("metadata-only change was not explained:\n%s", stderr.String())
	}
}

func TestDeployWatchFailureKeepsWatching(t *testing.T) {
	previousPoll := watchPollInterval
	watchPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { watchPollInterval = previousPoll })

	var deploys atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo":
			_, _ = w.Write([]byte(`{"app":{"slug":"demo","status":"running"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo/logs":
			w.Header().Set("Content-Type", "text/event-stream")
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/demo/deploy":
			if deploys.Add(1) == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"deploy failed: candidate did not become ready"}`))
				return
			}
			_, _ = w.Write([]byte(`{"slug":"demo","status":"running","deploy_count":2,"current_version":"v2"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	writeTestCLIConfig(t, srv.URL)

	dir := deployTestBundleDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	root, _, stderr := watchTestCommand(t, ctx, dir, srv.URL)
	done := make(chan error, 1)
	go func() { done <- root.Execute() }()
	waitForAtomicCount(t, &deploys, 1)
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("# fixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForAtomicCount(t, &deploys, 2)
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("watch returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not stop")
	}
	if !strings.Contains(stderr.String(), "watch process is still running") {
		t.Fatalf("failure did not explain recovery path:\n%s", stderr.String())
	}
}

func watchTestCommand(t *testing.T, ctx context.Context, dir, host string) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	resetFormatState(t)
	root := &cobra.Command{Use: "shinyhub", SilenceErrors: true}
	AddCommandsTo(root)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(stderr)
	for _, sub := range allSubcommands(root) {
		sub.SetOut(stdout)
		sub.SetErr(stderr)
	}
	root.SetContext(ctx)
	root.SetArgs([]string{"deploy", dir, "--slug", "demo", "--watch", "--watch-delay", "100ms", "--host", host, "-o", "table"})
	return root, stdout, stderr
}

func waitForAtomicCount(t *testing.T, value *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if value.Load() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("count = %d, want at least %d", value.Load(), want)
}

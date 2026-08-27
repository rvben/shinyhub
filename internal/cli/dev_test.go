package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestDevCommandPresentsOneModeAwareWorkflow(t *testing.T) {
	root := &cobra.Command{Use: "shinyhub"}
	AddCommandsTo(root)
	dev, _, err := root.Find([]string{"dev"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	dev.SetOut(&out)
	if err := dev.Help(); err != nil {
		t.Fatal(err)
	}
	help := out.String()
	for _, want := range []string{
		"Local development is the default", "--remote", "Local flags:", "Remote flags:",
		"shinyhub dev . --remote dev --slug sales-dev",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("dev help omitted %q:\n%s", want, help)
		}
	}
	if strings.Contains(help, "Global Flags:") || strings.Contains(help, "--host string") {
		t.Fatalf("dev help exposes the ambiguous global host selector:\n%s", help)
	}
}

func TestDevModeFlagsCannotSilentlyCrossModes(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "remote option locally", args: []string{"--create"}, want: "require remote development"},
		{name: "local option remotely", args: []string{"--remote", "dev", "--port", "8080"}, want: "apply only to local development"},
		{name: "empty remote", args: []string{"--remote", ""}, want: "requires a saved host name or server URL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newDevCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(tc.args)
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDevRejectsGlobalHostInFavorOfExplicitRemote(t *testing.T) {
	previousHost := hostFlagOverride
	t.Cleanup(func() { hostFlagOverride = previousHost })
	root := &cobra.Command{Use: "shinyhub"}
	AddCommandsTo(root)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"dev", "--host", "dev"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "does not select") || !strings.Contains(hintOf(err), "--remote <host>") {
		t.Fatalf("error = %v", err)
	}
}

func TestDevDefaultsLocalEvenWithAmbientRemoteHost(t *testing.T) {
	t.Setenv("SHINYHUB_HOST", "https://must-not-be-used.example.test")
	t.Setenv("SHINYHUB_TOKEN", "must-not-be-used")
	cmd := newDevCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{t.TempDir()})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "resolve launch") {
		t.Fatalf("error = %v, want local launch validation", err)
	}
	if strings.Contains(err.Error(), "credential") || strings.Contains(err.Error(), "must-not-be-used") {
		t.Fatalf("local dev consulted remote configuration: %v", err)
	}
}

func TestDevRemoteMapsToDurableWatchSession(t *testing.T) {
	resetFormatState(t)
	previousPoll := watchPollInterval
	previousHost := hostFlagOverride
	watchPollInterval = 10 * time.Millisecond
	t.Cleanup(func() {
		watchPollInterval = previousPoll
		hostFlagOverride = previousHost
	})

	var deploys atomic.Int32
	var attributed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo":
			_, _ = io.WriteString(w, `{"app":{"slug":"demo","status":"running","access":"private","deploy_count":1,"current_version":"v1"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/demo/deploy":
			deploys.Add(1)
			attributed.Store(r.Header.Get("X-Shinyhub-Deploy-Channel") == "watch" &&
				len(r.Header.Get("X-Shinyhub-Development-Session")) == 32 &&
				r.Header.Get("X-Shinyhub-Development-Target") == "existing")
			_, _ = io.WriteString(w, `{"slug":"demo","status":"running","access":"private","deploy_count":2,"current_version":"v2"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/end"):
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	writeTestCLIConfig(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cmd := newDevCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{deployTestBundleDir(t), "--remote", srv.URL, "--slug", "demo", "--watch-delay", "100ms"})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	waitForAtomicCount(t, &deploys, 1)
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("dev remote returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dev remote did not stop after cancellation")
	}
	if !attributed.Load() {
		t.Fatal("dev remote deployment lacked development-session attribution")
	}
	if hostFlagOverride != previousHost {
		t.Fatalf("remote host override leaked: got %q, want %q", hostFlagOverride, previousHost)
	}
}

func TestDevRemoteCreationPreflightsBeforeMutation(t *testing.T) {
	resetFormatState(t)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	writeTestCLIConfig(t, srv.URL)

	cmd := newDevCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{t.TempDir(), "--remote", srv.URL, "--create", "--slug", "empty-dev"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("invalid local source passed bundle validation")
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("invalid local source caused %d remote request(s)", got)
	}
}

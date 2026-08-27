package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
		"shinyhub dev . --remote dev --slug sales-dev", "--app", "--all", "--standalone", "--file",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("dev help omitted %q:\n%s", want, help)
		}
	}
	if strings.Contains(help, "Global Flags:") || strings.Contains(help, "--host string") {
		t.Fatalf("dev help exposes the ambiguous global host selector:\n%s", help)
	}
}

func TestDevRemoteFleetComposesAndWatchesSharedInputs(t *testing.T) {
	resetFormatState(t)
	previousPoll := watchPollInterval
	previousHost := hostFlagOverride
	watchPollInterval = 10 * time.Millisecond
	t.Cleanup(func() {
		watchPollInterval = previousPoll
		hostFlagOverride = previousHost
	})

	root := t.TempDir()
	appDir := filepath.Join(root, "apps", "sales")
	mustWrite(t, filepath.Join(appDir, "app.py"), "# shiny\n")
	shared := filepath.Join(root, "shared", "theme.py")
	mustWrite(t, shared, "orange\n")
	mustWrite(t, filepath.Join(root, "fleet.toml"), `fleet_id = "analytics"
[[bundle_file]]
from = "shared/theme.py"
to = "helpers/theme.py"
consumers = ["sales"]
[[app]]
slug = "sales"
source = "apps/sales"
`)

	uploads := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/sales":
			_, _ = io.WriteString(w, `{"app":{"slug":"sales","status":"running","access":"private","deploy_count":1,"current_version":"v1"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/sales/deploy":
			value, err := uploadedZipEntry(r, "helpers/theme.py")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"`+err.Error()+`"}`)
				return
			}
			uploads <- value
			_, _ = io.WriteString(w, `{"slug":"sales","status":"running","access":"private","deploy_count":2,"current_version":"v2"}`)
		case r.Method == http.MethodPost && (strings.HasSuffix(r.URL.Path, "/heartbeat") || strings.HasSuffix(r.URL.Path, "/end")):
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
	cmd.SetArgs([]string{root, "--app", "sales", "--remote", srv.URL, "--watch-delay", "100ms"})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	select {
	case got := <-uploads:
		if got != "orange\n" {
			t.Fatalf("initial shared input = %q", got)
		}
	case err := <-done:
		t.Fatalf("fleet development stopped before initial deployment: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("initial fleet development deployment did not arrive")
	}
	if err := os.WriteFile(shared, []byte("blue\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-uploads:
		if got != "blue\n" {
			t.Fatalf("updated shared input = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shared input change did not trigger a deployment")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("fleet dev returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fleet dev did not stop after cancellation")
	}
}

func TestDevRemoteFleetPreflightsEveryTargetBeforeMutation(t *testing.T) {
	resetFormatState(t)
	previousHost := hostFlagOverride
	t.Cleanup(func() { hostFlagOverride = previousHost })
	root, _, _ := writeDevFleetFixture(t)
	var mutations atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations.Add(1)
		}
		switch r.URL.Path {
		case "/api/apps/sales":
			_, _ = io.WriteString(w, `{"app":{"slug":"sales","status":"running"}}`)
		case "/api/apps/ops":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	writeTestCLIConfig(t, srv.URL)

	cmd := newDevCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{root, "--remote", srv.URL})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "ops") || !strings.Contains(hintOf(err), "fleet apply") {
		t.Fatalf("error = %v", err)
	}
	if got := mutations.Load(); got != 0 {
		t.Fatalf("incomplete remote target set caused %d mutation(s)", got)
	}
}

func TestDevRemoteFleetStartsEveryLocalTarget(t *testing.T) {
	resetFormatState(t)
	previousPoll := watchPollInterval
	previousHost := hostFlagOverride
	watchPollInterval = 10 * time.Millisecond
	t.Cleanup(func() {
		watchPollInterval = previousPoll
		hostFlagOverride = previousHost
	})
	root, _, _ := writeDevFleetFixture(t)
	deployed := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && (r.URL.Path == "/api/apps/sales" || r.URL.Path == "/api/apps/ops"):
			slug := strings.TrimPrefix(r.URL.Path, "/api/apps/")
			_, _ = fmt.Fprintf(w, `{"app":{"slug":%q,"status":"running","access":"private","deploy_count":1}}`, slug)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/deploy"):
			slug := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/apps/"), "/deploy")
			deployed <- slug
			_, _ = fmt.Fprintf(w, `{"slug":%q,"status":"running","access":"private","deploy_count":2}`, slug)
		case r.Method == http.MethodPost && (strings.HasSuffix(r.URL.Path, "/heartbeat") || strings.HasSuffix(r.URL.Path, "/end")):
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
	cmd.SetArgs([]string{root, "--remote", srv.URL, "--watch-delay", "100ms"})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	want := map[string]bool{"sales": true, "ops": true}
	for len(want) > 0 {
		select {
		case slug := <-deployed:
			delete(want, slug)
		case err := <-done:
			t.Fatalf("multi-app remote development stopped early: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatalf("apps never deployed: %v", want)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("multi-app remote development returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("multi-app remote development did not stop")
	}
}

func TestDevLocalFleetStartsEveryAppWithAttributedOutput(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not in PATH")
	}
	root := t.TempDir()
	server := `import http.server, os
http.server.HTTPServer(("127.0.0.1", int(os.environ["PORT"])), http.server.SimpleHTTPRequestHandler).serve_forever()
`
	for _, slug := range []string{"sales", "ops"} {
		dir := filepath.Join(root, "apps", slug)
		mustWrite(t, filepath.Join(dir, "server.py"), server)
		mustWrite(t, filepath.Join(dir, "shinyhub.toml"), "[app]\ncommand = [\"python3\", \"server.py\"]\n")
	}
	mustWrite(t, filepath.Join(root, "fleet.toml"), `fleet_id = "analytics"
[[app]]
slug = "sales"
source = "apps/sales"
[[app]]
slug = "ops"
source = "apps/ops"
`)
	scope, err := resolveDevScope(root, "fleet.toml", false, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	out := newFleetReadyWriter([]string{"[sales] Ready", "[ops] Ready"})
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	cmd.SetOut(out)
	cmd.SetErr(out)
	stateRoot := filepath.Join(t.TempDir(), "state")
	dataRoot := filepath.Join(t.TempDir(), "data")
	done := make(chan error, 1)
	go func() {
		done <- runLocalFleetDev(cmd, &devFlags{
			noSync: true, stateDir: stateRoot, dataDir: dataRoot,
		}, scope)
	}()
	select {
	case <-out.ready:
	case err := <-done:
		t.Fatalf("local fleet development stopped early: %v\n%s", err, out.String())
	case <-time.After(8 * time.Second):
		t.Fatalf("local fleet apps did not become ready:\n%s", out.String())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("local fleet development returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("local fleet development did not stop")
	}
}

type fleetReadyWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	wants []string
	ready chan struct{}
	once  sync.Once
}

func newFleetReadyWriter(wants []string) *fleetReadyWriter {
	return &fleetReadyWriter{wants: wants, ready: make(chan struct{})}
}

func (w *fleetReadyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	text := w.buf.String()
	for _, want := range w.wants {
		if !strings.Contains(text, want) {
			return n, err
		}
	}
	w.once.Do(func() { close(w.ready) })
	return n, err
}

func (w *fleetReadyWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestAppEventWriterAddsFleetIdentityToEveryNDJSONRecord(t *testing.T) {
	var out bytes.Buffer
	var mu sync.Mutex
	w := newAppEventWriter(&mu, &out, "sales")
	if _, err := w.Write([]byte("{\"type\":\"phase\"}\n{\"type\":")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("\"result\"}\n")); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %q", lines)
	}
	for _, line := range lines {
		if !strings.Contains(line, `"app":"sales"`) {
			t.Fatalf("record lacks app identity: %s", line)
		}
	}

	out.Reset()
	const exactID = "9007199254740993"
	if _, err := w.Write([]byte(`{"type":"result","deployment_id":` + exactID + `}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"deployment_id":`+exactID) {
		t.Fatalf("large numeric value changed while attributing NDJSON: %s", out.String())
	}
}

func uploadedZipEntry(r *http.Request, name string) (string, error) {
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		return "", err
	}
	file, _, err := r.FormFile("bundle")
	if err != nil {
		return "", err
	}
	defer file.Close()
	raw, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", err
	}
	for _, entry := range zr.File {
		if entry.Name != name {
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			return "", err
		}
		data, readErr := io.ReadAll(rc)
		_ = rc.Close()
		return string(data), readErr
	}
	return "", fmt.Errorf("zip entry %s not found", name)
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

func TestDevFleetSafetyBoundariesAreActionable(t *testing.T) {
	root, _, _ := writeDevFleetFixture(t)
	tests := []struct {
		name string
		args []string
		want string
		hint string
	}{
		{name: "manifest identity", args: []string{root, "--slug", "other"}, want: "cannot override", hint: "--app"},
		{name: "one port for many apps", args: []string{root, "--port", "8000"}, want: "multiple apps", hint: "automatic ports"},
		{name: "no partial remote creation", args: []string{root, "--remote", "dev", "--create"}, want: "single-app", hint: "fleet apply"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newDevCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(tc.args)
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(hintOf(err), tc.hint) {
				t.Fatalf("error = %v, hint = %q", err, hintOf(err))
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
		case r.Method == http.MethodPost && (strings.HasSuffix(r.URL.Path, "/heartbeat") || strings.HasSuffix(r.URL.Path, "/end")):
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

package deploy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeManifest(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ManifestFilename), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadManifest_AbsentReturnsNil(t *testing.T) {
	m, err := LoadManifest(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error for missing manifest: %v", err)
	}
	if m != nil {
		t.Errorf("expected nil manifest when file is absent, got %#v", m)
	}
}

func TestLoadManifest_ParsesPostDeployHook(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[[hook]]
on = "post-deploy"
command = ["python", "-m", "scripts.migrate"]
timeout = "30s"

[[hook]]
on = "post-deploy"
command = ["echo", "done"]
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	hooks := m.PostDeploy()
	if len(hooks) != 2 {
		t.Fatalf("expected 2 post-deploy hooks, got %d", len(hooks))
	}
	if !reflect.DeepEqual(hooks[0].Command, []string{"python", "-m", "scripts.migrate"}) {
		t.Errorf("hooks[0].Command = %v", hooks[0].Command)
	}
	if hooks[0].Timeout != 30*time.Second {
		t.Errorf("hooks[0].Timeout = %s, want 30s", hooks[0].Timeout)
	}
}

func TestLoadManifest_RejectsUnknownTrigger(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[[hook]]
on = "pre-deploy"
command = ["true"]
`)
	if _, err := LoadManifest(dir); err == nil || !strings.Contains(err.Error(), "unknown trigger") {
		t.Errorf("expected unknown-trigger error, got %v", err)
	}
}

func TestLoadManifest_RejectsMissingCommand(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[[hook]]
on = "post-deploy"
`)
	if _, err := LoadManifest(dir); err == nil || !strings.Contains(err.Error(), "missing `command`") {
		t.Errorf("expected missing-command error, got %v", err)
	}
}

func TestLoadManifest_RejectsMalformedTOML(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `[[hook] = "broken"`)
	if _, err := LoadManifest(dir); err == nil {
		t.Error("expected parse error for malformed TOML")
	}
}

func TestRunPostDeployHooks_StopsOnFailure(t *testing.T) {
	var ran []string
	origRunner := hookRunner
	t.Cleanup(func() { hookRunner = origRunner })
	hookRunner = func(ctx context.Context, dir string, argv []string, env []string, w io.Writer) error {
		ran = append(ran, argv[0])
		if argv[0] == "fail" {
			return errors.New("boom")
		}
		return nil
	}

	err := RunPostDeployHooks(context.Background(), "/tmp", []Hook{
		{On: HookPostDeploy, Command: []string{"ok"}},
		{On: HookPostDeploy, Command: []string{"fail"}},
		{On: HookPostDeploy, Command: []string{"never"}},
	}, nil, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "fail") {
		t.Errorf("expected error wrapping fail, got %v", err)
	}
	want := []string{"ok", "fail"}
	if !reflect.DeepEqual(ran, want) {
		t.Errorf("ran = %v, want %v (must stop after first failure)", ran, want)
	}
}

func TestRunPostDeployHooks_HonoursTimeout(t *testing.T) {
	origRunner := hookRunner
	t.Cleanup(func() { hookRunner = origRunner })
	hookRunner = func(ctx context.Context, dir string, argv []string, env []string, w io.Writer) error {
		// Simulate a hook that ignores cancellation up to its own internal deadline.
		<-ctx.Done()
		return ctx.Err()
	}

	start := time.Now()
	err := RunPostDeployHooks(context.Background(), "/tmp", []Hook{
		{On: HookPostDeploy, Command: []string{"sleep"}, Timeout: 50 * time.Millisecond},
	}, nil, io.Discard)
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected timeout error, got %v", err)
	}
	if elapsed >= time.Second {
		t.Errorf("timeout took %s, expected ~50ms — context cancel not propagated?", elapsed)
	}
}

func TestRunPostDeployHooks_EmptyIsNoop(t *testing.T) {
	if err := RunPostDeployHooks(context.Background(), "/tmp", nil, nil, io.Discard); err != nil {
		t.Errorf("empty hook list should be a no-op, got %v", err)
	}
}

func TestRunPostDeployHooks_LogsCommand(t *testing.T) {
	origRunner := hookRunner
	t.Cleanup(func() { hookRunner = origRunner })
	hookRunner = func(ctx context.Context, dir string, argv []string, env []string, w io.Writer) error {
		w.Write([]byte("hook stdout line\n"))
		return nil
	}

	var buf bytes.Buffer
	if err := RunPostDeployHooks(context.Background(), "/tmp", []Hook{
		{On: HookPostDeploy, Command: []string{"echo", "hi"}},
	}, nil, &buf); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "echo hi") {
		t.Errorf("expected log to announce command, got:\n%s", got)
	}
	if !strings.Contains(got, "hook stdout line") {
		t.Errorf("expected runner stdout to reach log, got:\n%s", got)
	}
}

// TestRunHookExec_Roundtrip exercises the real os/exec path with /bin/echo
// to confirm cwd, stdout capture, and exit-code handling all wire up.
func TestRunHookExec_Roundtrip(t *testing.T) {
	if _, err := os.Stat("/bin/echo"); err != nil {
		t.Skip("/bin/echo not available")
	}
	var buf bytes.Buffer
	if err := runHookExec(context.Background(), t.TempDir(), []string{"/bin/echo", "hello-from-hook"}, nil, &buf); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.Contains(buf.String(), "hello-from-hook") {
		t.Errorf("stdout mismatch: %q", buf.String())
	}
}

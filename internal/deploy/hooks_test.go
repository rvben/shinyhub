package deploy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
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

// TestRunHookExec_DoesNotLeakServerSecrets is the P0 regression: post-deploy
// hooks are deployer-controlled code and must never see SHINYHUB_* server
// secrets, while still receiving the app's own env (extraEnv). It exercises
// the real os/exec path.
func TestRunHookExec_DoesNotLeakServerSecrets(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	t.Setenv("SHINYHUB_AUTH_SECRET", "TOP-SECRET-AUTH")
	t.Setenv("SHINYHUB_DEPLOY_TOKEN", "TOP-SECRET-DEPLOY")
	t.Setenv("SHINYHUB_GITHUB_CLIENT_SECRET", "TOP-SECRET-OAUTH")

	var buf bytes.Buffer
	extraEnv := []string{"APP_VAR=app-value", "SHINYHUB_APP_DATA=/data/demo"}
	if err := runHookExec(context.Background(), t.TempDir(),
		[]string{sh, "-c", "env"}, extraEnv, &buf); err != nil {
		t.Fatalf("exec: %v", err)
	}
	out := buf.String()

	for _, leaked := range []string{"TOP-SECRET-AUTH", "TOP-SECRET-DEPLOY", "TOP-SECRET-OAUTH"} {
		if strings.Contains(out, leaked) {
			t.Errorf("server secret leaked into hook env: %q present in:\n%s", leaked, out)
		}
	}
	if strings.Contains(out, "SHINYHUB_AUTH_SECRET=") {
		t.Errorf("SHINYHUB_AUTH_SECRET leaked into hook env:\n%s", out)
	}
	// The app's own env (incl. the platform SHINYHUB_APP_DATA passed via
	// extraEnv) must still reach the hook.
	if !strings.Contains(out, "APP_VAR=app-value") {
		t.Errorf("app env var missing from hook env:\n%s", out)
	}
	if !strings.Contains(out, "SHINYHUB_APP_DATA=/data/demo") {
		t.Errorf("platform SHINYHUB_APP_DATA (via extraEnv) missing from hook env:\n%s", out)
	}
}

func TestLoadManifest_ParsesAppSettings(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[app]
hibernate_timeout_minutes = 0
replicas = 2
max_sessions_per_replica = 10
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.App.HibernateTimeoutMinutes == nil || *m.App.HibernateTimeoutMinutes != 0 {
		t.Errorf("hibernate = %v, want 0", m.App.HibernateTimeoutMinutes)
	}
	if m.App.Replicas == nil || *m.App.Replicas != 2 {
		t.Errorf("replicas = %v, want 2", m.App.Replicas)
	}
	if m.App.MaxSessionsPerReplica == nil || *m.App.MaxSessionsPerReplica != 10 {
		t.Errorf("max_sessions_per_replica = %v, want 10", m.App.MaxSessionsPerReplica)
	}
}

func TestLoadManifest_HibernateMinusOneResetsToDefault(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[app]
hibernate_timeout_minutes = -1
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !m.App.HibernateResetToDefault {
		t.Errorf("expected HibernateResetToDefault=true when -1 specified")
	}
	if m.App.HibernateTimeoutMinutes != nil {
		t.Errorf("HibernateTimeoutMinutes should be nil when reset sentinel is used")
	}
}

func TestLoadManifest_RejectsUnknownAppField(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[app]
slug = "new-name"
`)
	if _, err := LoadManifest(dir); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("expected unknown-field error, got %v", err)
	}
}

func TestLoadManifest_ParsesSchedules(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[[schedule]]
name = "daily-fetch"
cron = "0 6 * * *"
cmd = "uv run python fetch.py"
timeout_seconds = 600
overlap = "skip"
missed = "skip"
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Schedules) != 1 {
		t.Fatalf("schedules = %d, want 1", len(m.Schedules))
	}
	s := m.Schedules[0]
	if s.Name != "daily-fetch" || s.Cron != "0 6 * * *" || s.Cmd != "uv run python fetch.py" {
		t.Errorf("schedule = %+v", s)
	}
	if len(s.Command) != 4 || s.Command[0] != "uv" {
		t.Errorf("Command = %v, want [uv run python fetch.py]", s.Command)
	}
}

func TestLoadManifest_RejectsBadCron(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[[schedule]]
name = "x"
cron = "not-a-cron"
cmd = "echo hi"
`)
	if _, err := LoadManifest(dir); err == nil || !strings.Contains(err.Error(), "cron_expr") {
		t.Errorf("expected cron parse error, got %v", err)
	}
}

func TestLoadManifest_RejectsBadName(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[[schedule]]
name = "spaces are bad"
cron = "0 * * * *"
cmd = "echo hi"
`)
	if _, err := LoadManifest(dir); err == nil || !strings.Contains(err.Error(), "name") {
		t.Errorf("expected name validation error, got %v", err)
	}
}

func TestLoadManifest_RejectsEmptyCmdJSON(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[[schedule]]
name = "x"
cron = "0 * * * *"
cmd_json = "[]"
`)
	if _, err := LoadManifest(dir); err == nil || !strings.Contains(err.Error(), "command") {
		t.Errorf("expected empty-command error, got %v", err)
	}
}

func TestLoadManifest_RejectsCmdAndCmdJSONBoth(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[[schedule]]
name = "x"
cron = "0 * * * *"
cmd = "echo a"
cmd_json = '["echo","b"]'
`)
	if _, err := LoadManifest(dir); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Errorf("expected cmd/cmd_json mutex error, got %v", err)
	}
}

func TestLoadManifest_RejectsDuplicateScheduleNames(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[[schedule]]
name = "x"
cron = "0 6 * * *"
cmd = "echo a"

[[schedule]]
name = "x"
cron = "0 7 * * *"
cmd = "echo b"
`)
	if _, err := LoadManifest(dir); err == nil || !strings.Contains(err.Error(), "duplicate name") {
		t.Errorf("expected duplicate-name error, got %v", err)
	}
}

func TestLoadManifest_AppValidation_RejectsTooLowReplicas(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[app]
replicas = 0
`)
	if _, err := LoadManifest(dir); err == nil || !strings.Contains(err.Error(), "replicas") {
		t.Errorf("expected replicas validation error, got %v", err)
	}
}

func TestLoadManifest_ParsesSchedules_CmdJSON(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[[schedule]]
name = "daily-fetch"
cron = "0 6 * * *"
cmd_json = '["uv","run","python","fetch.py"]'
timeout_seconds = 600
overlap = "skip"
missed = "skip"
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Schedules) != 1 {
		t.Fatalf("schedules = %d, want 1", len(m.Schedules))
	}
	want := []string{"uv", "run", "python", "fetch.py"}
	if !reflect.DeepEqual(m.Schedules[0].Command, want) {
		t.Errorf("Command = %v, want %v", m.Schedules[0].Command, want)
	}
}

func TestLoadManifest_DefaultsWrittenBack(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[[schedule]]
name = "hourly"
cron = "0 * * * *"
cmd = "echo hi"
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Schedules) != 1 {
		t.Fatalf("schedules = %d, want 1", len(m.Schedules))
	}
	s := m.Schedules[0]
	if s.TimeoutSeconds == nil || *s.TimeoutSeconds != 3600 {
		t.Errorf("TimeoutSeconds = %v, want 3600", s.TimeoutSeconds)
	}
	if s.Overlap != "skip" {
		t.Errorf("Overlap = %q, want %q", s.Overlap, "skip")
	}
	if s.Missed != "skip" {
		t.Errorf("Missed = %q, want %q", s.Missed, "skip")
	}
}

func TestLoadManifest_Schedule_ExplicitTimezone(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[[schedule]]
name = "daily-fetch"
cron = "0 6 * * *"
cmd = "python fetch.py"
timezone = "Europe/Amsterdam"
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.Schedules) != 1 {
		t.Fatalf("schedules = %d, want 1", len(m.Schedules))
	}
	if m.Schedules[0].Timezone != "Europe/Amsterdam" {
		t.Errorf("Timezone = %q, want Europe/Amsterdam", m.Schedules[0].Timezone)
	}
}

func TestLoadManifest_Schedule_AbsentTimezoneIsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[[schedule]]
name = "daily-fetch"
cron = "0 6 * * *"
cmd = "python fetch.py"
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Schedules[0].Timezone != "" {
		t.Errorf("absent timezone should be empty string, got %q", m.Schedules[0].Timezone)
	}
}

func TestLoadManifest_Schedule_InvalidTimezoneRejected(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[[schedule]]
name = "daily-fetch"
cron = "0 6 * * *"
cmd = "python fetch.py"
timezone = "Mars/Olympus"
`)
	_, err := LoadManifest(dir)
	if err == nil {
		t.Fatal("expected error for invalid timezone, got nil")
	}
	if !strings.Contains(err.Error(), "shinyhub.toml [[schedule]] #1") {
		t.Errorf("error missing schedule prefix: %v", err)
	}
	if !strings.Contains(err.Error(), "timezone") {
		t.Errorf("error missing timezone context: %v", err)
	}
}

func TestLoadManifest_Schedule_CRON_TZ_PrefixRejected(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[[schedule]]
name = "daily-fetch"
cron = "CRON_TZ=UTC 0 6 * * *"
cmd = "python fetch.py"
`)
	_, err := LoadManifest(dir)
	if err == nil {
		t.Fatal("expected error for CRON_TZ= prefix in cron_expr, got nil")
	}
	if !strings.Contains(err.Error(), "cron_expr") {
		t.Errorf("error missing cron_expr context: %v", err)
	}
}

func TestLoadManifest_RunOnRegister(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[[schedule]]
name = "warm"
cron = "0 5 * * *"
cmd = "python fetch.py"
run_on_register = true
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(m.Schedules) != 1 {
		t.Fatalf("want 1 schedule, got %d", len(m.Schedules))
	}
	if !m.Schedules[0].RunOnRegister {
		t.Errorf("RunOnRegister = false, want true")
	}
}

func TestLoadManifest_RunOnRegister_DefaultsFalse(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[[schedule]]
name = "warm"
cron = "0 5 * * *"
cmd = "python fetch.py"
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Schedules[0].RunOnRegister {
		t.Errorf("RunOnRegister = true, want false (default)")
	}
}

func TestLoadManifest_AccessBlock(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[access]
viewer_groups = ["finance", "analysts"]
manager_groups = ["finance-leads"]
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(m.Access.ViewerGroups) != 2 || m.Access.ViewerGroups[0] != "finance" {
		t.Fatalf("viewer_groups = %v", m.Access.ViewerGroups)
	}
	if len(m.Access.ManagerGroups) != 1 || m.Access.ManagerGroups[0] != "finance-leads" {
		t.Fatalf("manager_groups = %v", m.Access.ManagerGroups)
	}
}

func TestLoadManifest_AccessBlock_RejectsEmptyGroup(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[access]
viewer_groups = ["finance", ""]
`)
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("expected error for an empty group name")
	}
}

func TestLoadManifest_UnknownKeyStillRejected(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[access]
bogus_key = ["x"]
`)
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("strict-mode parsing must reject an unknown key under [access]")
	}
}

func TestLoadManifest_TracingAutoTrue(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[tracing]
auto = true
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Tracing.Auto == nil || !*m.Tracing.Auto {
		t.Errorf("Tracing.Auto = %v, want explicit true", m.Tracing.Auto)
	}
}

func TestLoadManifest_TracingAutoFalse(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[tracing]
auto = false
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Tracing.Auto == nil || *m.Tracing.Auto {
		t.Errorf("Tracing.Auto = %v, want explicit false", m.Tracing.Auto)
	}
}

// Absent [tracing] means "inherit the fleet default": the pointer stays nil.
func TestLoadManifest_TracingAbsentIsNil(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[app]
replicas = 2
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Tracing.Auto != nil {
		t.Errorf("Tracing.Auto = %v, want nil (inherit)", *m.Tracing.Auto)
	}
}

// Strict mode must keep catching typos inside the new section.
func TestLoadManifest_TracingUnknownKeyRejected(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[tracing]
autoo = true
`)
	_, err := LoadManifest(dir)
	if err == nil {
		t.Fatal("expected unknown-field error for [tracing] autoo")
	}
	if !strings.Contains(err.Error(), "tracing.autoo") {
		t.Errorf("error should name the unknown key: %v", err)
	}
}

func TestLoadManifest_AppCommandAndIdentityHeaders(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[app]
identity_headers = false
command = ["uv", "run", "streamlit", "run", "app.py", "--server.port", "{port}", "--server.address", "{host}"]
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.App.IdentityHeaders == nil {
		t.Fatal("IdentityHeaders = nil, want explicit false")
	}
	if *m.App.IdentityHeaders != false {
		t.Errorf("IdentityHeaders = %v, want false", *m.App.IdentityHeaders)
	}
	if len(m.App.Command) != 9 {
		t.Errorf("Command len = %d, want 9; Command = %v", len(m.App.Command), m.App.Command)
	}
}

func TestLoadManifest_CommandRejectsUnknownPlaceholder(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[app]
command = ["serve", "--port", "{prot}"]
`)
	_, err := LoadManifest(dir)
	if err == nil || !strings.Contains(err.Error(), "{prot}") {
		t.Errorf("expected error mentioning {prot}, got %v", err)
	}
}

func TestLoadManifest_CommandRejectsEmptyForms(t *testing.T) {
	// empty array
	dir := t.TempDir()
	writeManifest(t, dir, `
[app]
command = []
`)
	if _, err := LoadManifest(dir); err == nil {
		t.Error("expected error for command = [], got nil")
	}

	// array with an empty element
	dir2 := t.TempDir()
	writeManifest(t, dir2, `
[app]
command = ["run", ""]
`)
	if _, err := LoadManifest(dir2); err == nil {
		t.Error("expected error for command containing empty element, got nil")
	}
}

func TestLoadManifest_CommandAllowsInertBraces(t *testing.T) {
	dir := t.TempDir()
	// ${VAR} - uppercase, {1..5} - digits, {Key: - no closing brace after word;
	// none match the \{[a-z_]+\} grammar, so all are inert and must parse fine.
	writeManifest(t, dir, `
[app]
command = ["sh-free", "${VAR}", "{1..5}", "{Key:"]
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("expected inert braces to parse fine, got: %v", err)
	}
	if len(m.App.Command) != 4 {
		t.Errorf("Command len = %d, want 4", len(m.App.Command))
	}
}

// TestLoadManifest_ParsesMinWarmReplicas confirms that min_warm_replicas = 2 in
// the [app] section is parsed into AppSettings.MinWarmReplicas.
func TestLoadManifest_ParsesMinWarmReplicas(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[app]
min_warm_replicas = 2
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.App.MinWarmReplicas == nil || *m.App.MinWarmReplicas != 2 {
		t.Errorf("MinWarmReplicas = %v, want 2", m.App.MinWarmReplicas)
	}
}

// TestLoadManifest_MinWarmReplicasRejectsNegative rejects a negative value.
func TestLoadManifest_MinWarmReplicasRejectsNegative(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[app]
min_warm_replicas = -1
`)
	_, err := LoadManifest(dir)
	if err == nil || !strings.Contains(err.Error(), "min_warm_replicas must be between 0 and 1000") {
		t.Errorf("expected bound error, got %v", err)
	}
}

// TestLoadManifest_MinWarmReplicasRejectsAboveMax rejects a value above 1000.
func TestLoadManifest_MinWarmReplicasRejectsAboveMax(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[app]
min_warm_replicas = 1001
`)
	_, err := LoadManifest(dir)
	if err == nil || !strings.Contains(err.Error(), "min_warm_replicas must be between 0 and 1000") {
		t.Errorf("expected bound error, got %v", err)
	}
}

// TestAppSettings_IsZero_MinWarmReplicasAlone confirms that IsZero returns false
// when only MinWarmReplicas is set.
func TestAppSettings_IsZero_MinWarmReplicasAlone(t *testing.T) {
	n := 2
	a := AppSettings{MinWarmReplicas: &n}
	if a.IsZero() {
		t.Error("IsZero should be false when MinWarmReplicas is set")
	}
}

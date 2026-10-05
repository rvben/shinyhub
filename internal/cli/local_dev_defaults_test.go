package cli

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/fleet"
)

func TestFleetLocalDefaultsEnvironmentPrecedence(t *testing.T) {
	t.Setenv("AWS_PROFILE", "host-profile")
	t.Setenv("SHINYHUB_APP_ENV_ALLOW", "sentinel")
	defaults := fleet.DevSettings{EnvAllow: []string{"AWS_PROFILE"}, Env: map[string]string{"FLEET_ONLY": "yes", "SETTING": "fleet"}, Seed: "missing"}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SETTING=app\nAWS_PROFILE=app-profile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := withFleetDevDefaults(localRunFlags{env: []string{"SETTING=cli"}}, defaults, false)
	got, err := resolveLocalRunEnvironment(dir, &f)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"AWS_PROFILE=app-profile", "FLEET_ONLY=yes", "SETTING=cli"}
	if !reflect.DeepEqual(got, want) || f.seed != "missing" {
		t.Fatalf("env=%v seed=%s", got, f.seed)
	}
	if os.Getenv("SHINYHUB_APP_ENV_ALLOW") != "sentinel" {
		t.Fatal("process-wide allowlist changed")
	}
	explicit := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(explicit, []byte("SETTING=explicit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f = withFleetDevDefaults(localRunFlags{envFile: explicit, seed: "never"}, defaults, true)
	got, err = resolveLocalRunEnvironment(dir, &f)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"AWS_PROFILE=host-profile", "FLEET_ONLY=yes", "SETTING=explicit"}
	if !reflect.DeepEqual(got, want) || f.seed != "never" {
		t.Fatalf("env=%v seed=%s", got, f.seed)
	}
}

func TestSeedFlagPreservesBareAndExplicitModes(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{nil, "never"}, {[]string{"--seed"}, "always"}, {[]string{"--seed=missing"}, "missing"},
		{[]string{"--seed=never"}, "never"}, {[]string{"--seed=false"}, "false"},
	} {
		cmd := newRunCmd()
		if err := cmd.Flags().Parse(test.args); err != nil {
			t.Fatal(err)
		}
		got, err := cmd.Flags().GetString("seed")
		if err != nil || got != test.want {
			t.Fatalf("args=%v seed=%s error=%v", test.args, got, err)
		}
	}
}

func TestLegacyFleetDiscoverySharesWorkspaceIdentity(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "alpha_user_dashboard")
	if err := os.Mkdir(app, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "shinyhub-fleet.toml")
	body := `fleet_id = "analytics"
[dev]
seed = "missing"
[[app]]
slug = "alpha-user-dashboard"
source = "./alpha_user_dashboard"
`
	if err := os.WriteFile(manifest, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	fromApp, err := resolveDevScope(app, "", false, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	fromRoot, err := resolveDevScope(root, "", false, false, false, []string{"alpha-user-dashboard"})
	if err != nil {
		t.Fatal(err)
	}
	if !fromApp.fleet() || !fromRoot.fleet() || fromApp.Targets[0].Manifest != fromRoot.Targets[0].Manifest || fromApp.Targets[0].Slug != "alpha-user-dashboard" || fromApp.Dev.Seed != "missing" {
		t.Fatalf("app=%+v root=%+v", fromApp, fromRoot)
	}
	modern := filepath.Join(root, "fleet.toml")
	if err := os.WriteFile(modern, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	both, err := resolveDevScope(root, "", false, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(both.Manifest) != "fleet.toml" {
		t.Fatal("existing modern-name precedence changed")
	}
}

func TestFleetDevDefaultsSeedOnceWithProductionLocalContext(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	t.Setenv("AWS_PROFILE", "host-profile")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "must-not-inherit")
	t.Setenv("SHINYHUB_APP_ENV_ALLOW", "")
	root := t.TempDir()
	app := filepath.Join(root, "alpha_user_dashboard")
	server := `import http.server, os
assert os.environ["SHINYHUB_RUN_MODE"] == "local"
assert os.environ["SHINYHUB_APP_SLUG"] == "alpha-user-dashboard"
assert os.environ["AWS_PROFILE"] == "host-profile"
assert os.environ["IS_LOCAL"] == "1"
assert "AWS_SECRET_ACCESS_KEY" not in os.environ
assert open(os.path.join(os.environ["SHINYHUB_APP_DATA"], "result")).read() == "seeded"
http.server.HTTPServer(("127.0.0.1", int(os.environ["PORT"])), http.server.SimpleHTTPRequestHandler).serve_forever()
`
	producer := `test "$SHINYHUB_RUN_MODE" = local || exit 3
test "$AWS_PROFILE" = host-profile || exit 4
test "$IS_LOCAL" = 1 || exit 5
test -z "$AWS_SECRET_ACCESS_KEY" || exit 6
echo attempt >> "$SHINYHUB_APP_DATA/count"
printf seeded > "$SHINYHUB_APP_DATA/result"
`
	mustWrite(t, filepath.Join(app, "server.py"), server)
	mustWrite(t, filepath.Join(app, "producer.sh"), producer)
	mustWrite(t, filepath.Join(app, "shinyhub.toml"), `[app]
command = ["python3", "server.py"]
[[schedule]]
name = "refresh-data"
cron = "0 * * * *"
deploy_trigger = "bundle_change"
cmd = "sh producer.sh"
`)
	mustWrite(t, filepath.Join(root, "shinyhub-fleet.toml"), `fleet_id = "analytics"
[dev]
seed = "missing"
env_allow = ["AWS_PROFILE"]
env = { IS_LOCAL = "1" }
[[app]]
slug = "alpha-user-dashboard"
source = "./alpha_user_dashboard"
`)
	state := t.TempDir()
	for _, source := range []string{root, app} {
		ctx, cancel := context.WithCancel(context.Background())
		out := newFleetReadyWriter([]string{"Ready"})
		cmd := newDevCmd()
		cmd.SetContext(ctx)
		cmd.SetOut(out)
		cmd.SetErr(out)
		cmd.SetArgs([]string{source, "--no-sync", "--state-dir", state})
		done := make(chan error, 1)
		go func() { done <- cmd.Execute() }()
		select {
		case <-out.ready:
		case err := <-done:
			cancel()
			t.Fatalf("dev stopped: %v\n%s", err, out.String())
		case <-time.After(8 * time.Second):
			cancel()
			<-done
			t.Fatalf("dev did not start: %s", out.String())
		}
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(8 * time.Second):
			t.Fatal("dev did not stop")
		}
	}
	got, err := os.ReadFile(filepath.Join(state, "data", "count"))
	if err != nil || string(got) != "attempt\n" {
		t.Fatalf("default missing policy fetched again: count=%q error=%v", got, err)
	}
	// Explicit schedule execution is a refresh regardless of the fleet default.
	cmd := newScheduleCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"run", "--local", "refresh-data", root, "--no-sync", "--state-dir", state})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(filepath.Join(state, "data", "count"))
	if err != nil || string(got) != "attempt\nattempt\n" {
		t.Fatalf("explicit refresh skipped: %q %v", got, err)
	}
}

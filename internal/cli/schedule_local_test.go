package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScheduleLocalFleetUsesDevIdentityAndInputsWithoutServer(t *testing.T) {
	_, requests, _ := setupCLITest(t)
	root := t.TempDir()
	app := filepath.Join(root, "sales")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		filepath.Join(app, "shinyhub.toml"): `[app]
command = ["sh", "-c", "exit 99"]
[[schedule]]
name = "fetch"
cron = "0 * * * *"
cmd = "sh helpers/producer.sh"
`,
		filepath.Join(app, ".env"):         "AWS_PROFILE=app-profile\n",
		filepath.Join(root, "producer.sh"): "test \"$AWS_PROFILE\" = override || exit 4\nprintf '%s' \"$SHINYHUB_APP_SLUG\" > \"$SHINYHUB_APP_DATA/result\"\necho ready\n",
		filepath.Join(root, "fleet.toml"): `fleet_id = "analytics"
[[bundle_file]]
from = "producer.sh"
to = "helpers/producer.sh"
consumers = ["sales"]
[[app]]
slug = "sales"
source = "./sales"
`,
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	state := t.TempDir()
	cmd := newScheduleCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"run", "--local", "fetch", root, "--app", "sales", "--no-sync", "--state-dir", state, "--env", "AWS_PROFILE=override"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, &out)
	}
	if len(*requests) != 0 {
		t.Fatal("local schedule contacted server")
	}
	got, err := os.ReadFile(filepath.Join(state, "data", "result"))
	if err != nil || string(got) != "sales" {
		t.Fatalf("result=%q error=%v", got, err)
	}
	if !strings.Contains(out.String(), "[fetch] ready") {
		t.Fatalf("unlabelled output: %s", &out)
	}
}

func TestScheduleLocalPreservesExitCode(t *testing.T) {
	dir := t.TempDir()
	body := `[app]
command = ["sh", "-c", "exit 99"]
[[schedule]]
name = "fetch"
cron = "0 * * * *"
cmd = "sh -c 'exit 7'"
`
	if err := os.WriteFile(filepath.Join(dir, "shinyhub.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := newScheduleCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"run", "--local", "fetch", dir, "--no-sync", "--state-dir", t.TempDir()})
	err := cmd.Execute()
	var exit *ExitCodeError
	if !errors.As(err, &exit) || exit.Code != 7 {
		t.Fatalf("error=%v", err)
	}
}

func TestScheduleRunRejectsMixedModes(t *testing.T) {
	for _, args := range [][]string{
		{"run", "demo", "fetch", "--env", "X=y"},
		{"run", "--local", "fetch", "--follow"},
		{"run", "--local", ""},
	} {
		cmd := newScheduleCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		var exit *ExitCodeError
		if !errors.As(err, &exit) || exit.Kind != KindValidation {
			t.Fatalf("args=%v error=%v", args, err)
		}
	}
}

func TestScheduleLocalFleetPreflightsAllAppsBeforeProducing(t *testing.T) {
	for _, problem := range []string{"missing-schedule", "invalid-env", "reserved-env"} {
		t.Run(problem, func(t *testing.T) {
			root := t.TempDir()
			schedule := `
[[schedule]]
name = "fetch"
cron = "0 * * * *"
cmd = "sh -c 'echo ran > $SHINYHUB_APP_DATA/result'"
`
			for _, slug := range []string{"a", "b"} {
				app := filepath.Join(root, slug)
				if err := os.Mkdir(app, 0o755); err != nil {
					t.Fatal(err)
				}
				body := "[app]\ncommand = [\"sh\", \"-c\", \"exit 99\"]\n"
				if slug == "a" || problem != "missing-schedule" {
					body += schedule
				}
				if err := os.WriteFile(filepath.Join(app, "shinyhub.toml"), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				if slug == "b" && problem != "missing-schedule" {
					env := "malformed"
					if problem == "reserved-env" {
						env = "SHINYHUB_APP_DATA=/bad"
					}
					if err := os.WriteFile(filepath.Join(app, ".env"), []byte(env), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			fleet := `fleet_id = "test"
[[app]]
slug = "a"
source = "./a"
[[app]]
slug = "b"
source = "./b"
`
			if err := os.WriteFile(filepath.Join(root, "fleet.toml"), []byte(fleet), 0o644); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(t.TempDir(), "state")
			cmd := newScheduleCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"run", "--local", "fetch", root, "--all", "--state-dir", state, "--no-sync"})
			err := cmd.Execute()
			var exit *ExitCodeError
			if !errors.As(err, &exit) || exit.Kind != KindValidation {
				t.Fatalf("error=%v", err)
			}
			if _, err := os.Stat(state); !os.IsNotExist(err) {
				t.Fatalf("invalid fleet started producing: %v", err)
			}
		})
	}
}

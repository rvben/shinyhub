package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/fleet"
)

// Use an actual terminal descriptor: buffers cannot exercise terminal detection,
// dimensions, or the interaction between forced color and cursor movement.
func TestFleetProgressPTY(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a Unix PTY")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is needed for the stdlib PTY harness")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, ci, gitlab, force, noColor string
		live, color, jsonOutput          bool
	}{
		{name: "interactive", ci: "false", live: true, color: true},
		{name: "CI", ci: "true"},
		{name: "GitLab", gitlab: "true"},
		{name: "forced color in CI", ci: "true", force: "1", color: true},
		{name: "no color beats force in CI", ci: "true", force: "1", noColor: "1"},
		{name: "no color preserves live layout", ci: "false", noColor: "1", live: true},
		{name: "JSON stdout and live stderr", ci: "false", live: true, color: true, jsonOutput: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			mode := "text"
			if tc.jsonOutput {
				mode = "json"
			}
			cmd := exec.CommandContext(ctx, python, "-c", fleetPTYHarness, executable, mode)
			controlled := map[string]string{
				"SHINYHUB_FLEET_PTY_HELPER": mode,
				"SHINYHUB_FLEET_PTY_CI":     tc.ci, "SHINYHUB_FLEET_PTY_GITLAB_CI": tc.gitlab,
				"CI": tc.ci, "GITLAB_CI": tc.gitlab,
				"FORCE_COLOR": tc.force, "NO_COLOR": tc.noColor,
				"CLICOLOR": "", "CLICOLOR_FORCE": "",
				"TERM": "xterm-256color", "LANG": "en_US.UTF-8", "LC_ALL": "en_US.UTF-8",
			}
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if _, ok := controlled[key]; !ok {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			for key, value := range controlled {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			output, err := cmd.CombinedOutput()
			if err != nil {
				if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 77 {
					t.Skipf("PTY unavailable: %s", output)
				}
				t.Fatalf("PTY harness: %v\n%s", err, output)
			}
			var capture struct {
				Terminal string `json:"terminal"`
				Stdout   string `json:"stdout"`
			}
			if err := json.Unmarshal(output, &capture); err != nil {
				t.Fatalf("invalid harness result: %v\n%s", err, output)
			}
			sgr := regexp.MustCompile("\x1b\\[[0-9;]*m")
			if got := sgr.MatchString(capture.Terminal); got != tc.color {
				t.Fatalf("color = %v, want %v: %q", got, tc.color, capture.Terminal)
			}
			if got := strings.Contains(sgr.ReplaceAllString(capture.Terminal, ""), "\x1b"); got != tc.live {
				t.Fatalf("cursor control = %v, want %v: %q", got, tc.live, capture.Terminal)
			}
			for _, want := range []string{"demo", "server advisory"} {
				if !strings.Contains(capture.Terminal, want) {
					t.Fatalf("lost %q: %q", want, capture.Terminal)
				}
			}
			if tc.live && !strings.Contains(capture.Terminal, "Fleet finished") {
				t.Fatalf("missing settled frame: %q", capture.Terminal)
			}
			if !tc.live && !strings.Contains(sgr.ReplaceAllString(capture.Terminal, ""), "refresh started (run #42)") {
				t.Fatalf("missing durable event: %q", capture.Terminal)
			}
			if tc.jsonOutput && capture.Stdout != "{\"ok\":true}\n" {
				t.Fatalf("JSON stdout polluted by progress: %q", capture.Stdout)
			}
		})
	}
}

func TestFleetProgressPTYHelper(t *testing.T) {
	mode := os.Getenv("SHINYHUB_FLEET_PTY_HELPER")
	if mode == "" {
		return
	}
	// Package TestMain clears ambient CI markers for ordinary tests. Restore
	// this subprocess's explicit scenario before exercising production policy.
	t.Setenv("CI", os.Getenv("SHINYHUB_FLEET_PTY_CI"))
	t.Setenv("GITLAB_CI", os.Getenv("SHINYHUB_FLEET_PTY_GITLAB_CI"))
	var out io.Writer = os.Stdout
	if mode == "json" {
		out = os.Stderr
	}
	display := newFleetLiveDisplay(out, []fleet.AppDiff{{Slug: "demo"}})
	if display != nil {
		out = display
	}
	app, finish := beginFleetApp(out, "demo")
	if !updateFleetProgress(app, "Refreshing data", "refresh-data · run #42", time.Now().Add(time.Minute), false) {
		fmt.Fprintf(app, "demo/refresh-data: refresh %s (run #42)\n", stylerFor(app).yellow("started"))
	}
	fmt.Fprintln(app, "demo: server advisory")
	finish(applyResult{status: statusUnchanged})
	if display != nil {
		display.close()
	}
	if mode == "json" {
		if err := json.NewEncoder(os.Stdout).Encode(map[string]bool{"ok": true}); err != nil {
			t.Fatal(err)
		}
	}
	// The helper's test runner must not append PASS to the captured JSON document.
	os.Exit(0)
}

const fleetPTYHarness = `
import errno, json, os, subprocess, sys, tempfile
try:
    import fcntl, pty, struct, termios
    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
except (ImportError, OSError) as exc:
    print(exc)
    sys.exit(77)
with tempfile.TemporaryFile() as stdout:
    proc = subprocess.Popen([sys.argv[1], "-test.run=^TestFleetProgressPTYHelper$"],
                            stdout=stdout if sys.argv[2] == "json" else slave,
                            stderr=slave, close_fds=True)
    os.close(slave)
    chunks = []
    try:
        while True:
            try:
                data = os.read(master, 65536)
            except OSError as exc:
                if exc.errno == errno.EIO:
                    break
                raise
            if not data:
                break
            chunks.append(data)
    finally:
        os.close(master)
    code = proc.wait()
    stdout.seek(0)
    result = {"terminal": b"".join(chunks).decode("utf-8"),
              "stdout": stdout.read().decode("utf-8")}
    print(json.dumps(result))
    sys.exit(code)
`

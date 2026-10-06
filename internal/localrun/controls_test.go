package localrun

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const controlledServer = `import http.server, os, sys, time, signal
version = open("version.txt").read().strip()
if version == "bad": sys.exit(3)
if version == "slow": time.sleep(30)
class H(http.server.BaseHTTPRequestHandler):
    requests = 0
    def do_GET(self):
        H.requests += 1
        if version == "handoff" and H.requests == 2:
            os.kill(int(open(os.path.join(os.environ["SHINYHUB_APP_DATA"], "old.pid")).read()), signal.SIGKILL)
            self.send_response(503)
            self.end_headers()
            return
        body = version.encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, fmt, *args): pass
http.server.HTTPServer(("127.0.0.1", int(os.environ["PORT"])), H).serve_forever()
`

type controlledRun struct {
	t        *testing.T
	source   string
	controls chan Control
	events   chan Event
	done     chan error
	records  []Event
}

func startControlledRun(t *testing.T, version string, configure func(*Options)) *controlledRun {
	t.Helper()
	skipIfNoPython3(t)
	r := &controlledRun{t: t, source: t.TempDir(), controls: make(chan Control, 8), events: make(chan Event, 1024), done: make(chan error, 1)}
	for name, body := range map[string]string{"server.py": controlledServer, "version.txt": version, "shinyhub.toml": "[app]\ncommand = [\"python3\", \"server.py\"]\n"} {
		if err := os.WriteFile(filepath.Join(r.source, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	o := Options{BundleDir: r.source, StateDir: t.TempDir(), Slug: "controls-test", NoSync: true, Controls: r.controls, OnEvent: func(e Event) { r.events <- e }}
	if configure != nil {
		configure(&o)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-r.done:
			if err != nil {
				t.Errorf("runner shutdown: %v", err)
			}
		case <-time.After(8 * time.Second):
			t.Error("runner failed to join its children")
		}
	})
	go func() { r.done <- Run(ctx, o, io.Discard, io.Discard) }()
	return r
}

func (r *controlledRun) await(kind, phase string) Event {
	r.t.Helper()
	timer := time.NewTimer(6 * time.Second)
	defer timer.Stop()
	for {
		select {
		case e := <-r.events:
			r.records = append(r.records, e)
			if e.Type == kind && e.Phase == phase {
				return e
			}
		case <-timer.C:
			r.t.Fatalf("timed out waiting for %s/%s: %+v", kind, phase, r.records)
		}
	}
}

func (r *controlledRun) write(version string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.source, "version.txt"), []byte(version), 0644); err != nil {
		r.t.Fatal(err)
	}
}

func readControlledURL(t *testing.T, url string) (int, string) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func (r *controlledRun) pausedForWatchCycle() {
	r.t.Helper()
	timer := time.NewTimer(1200 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case e := <-r.events:
			r.records = append(r.records, e)
			if e.Type == "process" && e.Phase == "started" || e.Type == "phase" && e.Phase == "ready" {
				r.t.Fatalf("stopped app restarted on a save: %+v", e)
			}
		case <-timer.C:
			return
		}
	}
}

func TestDevelopmentStopResumeUsesLatestSourceAndStableURL(t *testing.T) {
	r := startControlledRun(t, "v1", nil)
	first := r.await("phase", "ready")
	_, revision := readControlledURL(t, first.URL+"__shinyhub_dev_revision")
	r.controls <- Stop
	r.await("phase", "stopped")
	if status, body := readControlledURL(t, first.URL); status != 503 || !strings.Contains(body, "not running") {
		t.Fatalf("stopped route: %d %q", status, body)
	}
	r.write("v2")
	r.controls <- Restart // A restart must not implicitly resume a stopped app.
	r.pausedForWatchCycle()
	if _, got := readControlledURL(t, first.URL+"__shinyhub_dev_revision"); got != revision {
		t.Fatal("stop/save advanced browser revision")
	}
	r.controls <- Resume
	ready := r.await("phase", "ready")
	if ready.URL != first.URL || ready.Generation <= first.Generation {
		t.Fatalf("resume changed URL or failed to advance generation: %+v", ready)
	}
	if status, body := readControlledURL(t, ready.URL); status != 200 || body != "v2" {
		t.Fatalf("resume did not serve latest source: %d %q", status, body)
	}
}

func TestDevelopmentRestartKeepsHealthyAppOnFailure(t *testing.T) {
	r := startControlledRun(t, "v1", nil)
	first := r.await("phase", "ready")
	_, revision := readControlledURL(t, first.URL+"__shinyhub_dev_revision")
	r.controls <- Restart
	second := r.await("phase", "ready")
	if second.Attempt <= first.Attempt || second.Generation <= first.Generation || second.URL != first.URL {
		t.Fatalf("restart was not a fresh healthy replacement: %+v", second)
	}
	_, healthyRevision := readControlledURL(t, first.URL+"__shinyhub_dev_revision")
	r.write("bad")
	r.await("phase", "failed")
	r.controls <- Restart
	r.await("phase", "failed")
	if status, body := readControlledURL(t, first.URL); status != 200 || body != "v1" {
		t.Fatalf("failed restart lost healthy app: %d %q", status, body)
	}
	_, after := readControlledURL(t, first.URL+"__shinyhub_dev_revision")
	if revision == after {
		t.Fatal("successful restart failed to refresh browsers")
	}
	if after != healthyRevision {
		t.Fatal("failed restart advanced browser revision")
	}
	r.controls <- Stop
	r.await("phase", "stopped")
	r.controls <- Resume
	r.await("phase", "failed")
	if status, _ := readControlledURL(t, first.URL); status != 503 {
		t.Fatalf("failed resume route status %d", status)
	}
	r.write("v3")
	r.await("phase", "ready")
	if _, body := readControlledURL(t, first.URL); body != "v3" {
		t.Fatalf("save did not recover failed resume: %q", body)
	}
}

func TestDevelopmentStopCancelsPendingStartup(t *testing.T) {
	for _, initial := range []bool{true, false} {
		t.Run(fmt.Sprint("initial=", initial), func(t *testing.T) {
			version := "v1"
			if initial {
				version = "slow"
			}
			r := startControlledRun(t, version, nil)
			if !initial {
				r.await("phase", "ready")
				r.write("slow")
			}
			started := r.await("process", "started")
			r.controls <- Stop
			r.await("phase", "stopped")
			if err := syscall.Kill(started.PID, 0); err == nil {
				t.Fatal("stopped startup process is still alive")
			}
			r.write("v2")
			r.pausedForWatchCycle()
			r.controls <- Resume
			r.await("phase", "ready")
		})
	}
}

func TestDevelopmentCrashKeepsControlsAvailable(t *testing.T) {
	r := startControlledRun(t, "v1", nil)
	r.await("phase", "ready")
	var pid int
	for _, e := range r.records {
		if e.Type == "process" && e.Phase == "started" {
			pid = e.PID
		}
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	r.await("phase", "exited")
	r.controls <- Restart
	r.await("phase", "ready")
}

func TestDevelopmentOldProcessExitDoesNotDisableCandidatePublicProbe(t *testing.T) {
	data := t.TempDir()
	r := startControlledRun(t, "v1", func(o *Options) { o.DataDir = data })
	first := r.await("phase", "ready")
	var pid int
	for _, e := range r.records {
		if e.Type == "process" && e.Phase == "started" {
			pid = e.PID
		}
	}
	if err := os.WriteFile(filepath.Join(data, "old.pid"), []byte(fmt.Sprint(pid)), 0600); err != nil {
		t.Fatal(err)
	}
	r.write("handoff")
	ready := r.await("phase", "ready")
	if ready.Generation <= first.Generation || ready.URL != first.URL {
		t.Fatalf("candidate rejected after old process exit: %+v", ready)
	}
	if status, body := readControlledURL(t, first.URL); status != 200 || body != "handoff" {
		t.Fatalf("replacement did not serve: %d %q", status, body)
	}
}

func TestDevelopmentRestartAndResumeDoNotRepeatSeed(t *testing.T) {
	data := t.TempDir()
	r := startControlledRun(t, "v1", func(o *Options) {
		o.Seed, o.DataDir = true, data
		path := filepath.Join(o.BundleDir, "shinyhub.toml")
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		_, err = f.WriteString(`
[[schedule]]
name = "seed"
cron = "0 * * * *"
deploy_trigger = "bundle_change"
cmd = "sh -c 'echo seeded >> $SHINYHUB_APP_DATA/count'"
`)
		if err != nil {
			t.Fatal(err)
		}
	})
	r.await("phase", "ready")
	r.controls <- Restart
	r.await("phase", "ready")
	r.controls <- Stop
	r.await("phase", "stopped")
	r.controls <- Resume
	r.await("phase", "ready")
	count, err := os.ReadFile(filepath.Join(data, "count"))
	if err != nil || string(count) != "seeded\n" {
		t.Fatalf("seed repeated across controls: %q (%v)", count, err)
	}
}

func TestDevelopmentInvocationPreservesChangedDependencyInputs(t *testing.T) {
	skipIfNoPython3(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	source, state, tools := t.TempDir(), t.TempDir(), t.TempDir()
	count := filepath.Join(t.TempDir(), "syncs")
	uv := `#!/bin/sh
case "$1" in
  sync) printf 'sync\n' >> "$SYNC_RECORD"; mkdir -p .venv ;;
  run) exec "$TEST_PYTHON" app.py ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(tools, "uv"), []byte(uv), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	for name, body := range map[string]string{"app.py": controlledServer, "version.txt": "v1", "pyproject.toml": "[project]\nname = \"test\"\nversion = \"0.1.0\"\ndependencies = []\n"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	run := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		ready := false
		err := Run(ctx, Options{BundleDir: source, StateDir: state, Slug: "dependencies", Env: []string{"SYNC_RECORD=" + count, "TEST_PYTHON=" + python}, OnEvent: func(e Event) {
			if e.Type == "phase" && e.Phase == "ready" {
				ready = true
				cancel()
			}
		}}, io.Discard, io.Discard)
		if err != nil || !ready {
			t.Fatalf("dependency fixture failed to launch: ready=%v err=%v", ready, err)
		}
	}
	run()
	if err := os.WriteFile(filepath.Join(source, "pyproject.toml"), []byte("[project]\nname = \"test\"\nversion = \"0.2.0\"\ndependencies = []\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run()
	got, err := os.ReadFile(count)
	if err != nil || string(got) != "sync\nsync\n" {
		t.Fatalf("changed dependency inputs skipped preparation: %q (%v)", got, err)
	}
}

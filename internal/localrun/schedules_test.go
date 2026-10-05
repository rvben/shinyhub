package localrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deploy"
)

func scheduleFixture(t *testing.T, schedules string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shinyhub.toml"), []byte("[app]\ncommand = [\"sh\", \"-c\", \"exit 99\"]\n"+schedules), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLocalScheduleUsesWorkspaceDataAndExplicitEnvironment(t *testing.T) {
	dir := scheduleFixture(t, `
[[schedule]]
name = "fetch"
cron = "0 * * * *"
disabled = true
cmd = "sh producer.sh"
`)
	if err := os.WriteFile(filepath.Join(dir, "producer.sh"), []byte(`test "$SHINYHUB_APP_SLUG" = fixture || exit 3
test "$AWS_PROFILE" = development || exit 4
test -z "$AWS_SECRET_ACCESS_KEY" || exit 5
printf '%s' "$PWD" > "$SHINYHUB_APP_DATA/workspace"
printf 'first\nsecond\n'
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_SECRET_ACCESS_KEY", "must-not-inherit")
	t.Setenv("SHINYHUB_APP_ENV_ALLOW", "")
	state := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{BundleDir: dir, StateDir: state, Slug: "fixture", ScheduleName: "fetch", NoSync: true, Env: []string{"AWS_PROFILE=development"}}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(state, "data", "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	expected, err := filepath.EvalSymlinks(filepath.Join(state, "bundles", "0"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != expected {
		t.Fatalf("cwd = %q", got)
	}
	if !strings.Contains(stdout.String(), "[fetch] first\n[fetch] second\n") || !strings.Contains(stderr.String(), "disabled") {
		t.Fatalf("stdout=%s stderr=%s", &stdout, &stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "data")); !os.IsNotExist(err) {
		t.Fatal("source was modified")
	}
}

func TestLocalScheduleFailuresAndTimeout(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		code          int
	}{
		{"exit", "exit 7", 7}, {"timeout", "sleep 30", 124},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := scheduleFixture(t, "\n[[schedule]]\nname = \"fetch\"\ncron = \"0 * * * *\"\ntimeout_seconds = 1\ncmd = \"sh -c '"+tc.command+"'\"\n")
			started := time.Now()
			err := Run(context.Background(), Options{BundleDir: dir, StateDir: t.TempDir(), ScheduleName: "fetch", NoSync: true}, io.Discard, io.Discard)
			var jobErr *ScheduleError
			if !errors.As(err, &jobErr) || jobErr.Code != tc.code {
				t.Fatalf("error=%v, want exit %d", err, tc.code)
			}
			if time.Since(started) > 5*time.Second {
				t.Fatal("job process group did not stop promptly")
			}
		})
	}
}

func TestLocalScheduleUnknownName(t *testing.T) {
	dir := scheduleFixture(t, "")
	err := Run(context.Background(), Options{BundleDir: dir, StateDir: t.TempDir(), ScheduleName: "missing", NoSync: true}, io.Discard, io.Discard)
	var validation *ValidationError
	if !errors.As(err, &validation) || !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("error = %v", err)
	}
}

func TestSeedStopsBeforeAppAndLaterSchedulesOnFailure(t *testing.T) {
	dir := scheduleFixture(t, `
[[schedule]]
name = "disabled"
cron = "0 * * * *"
deploy_trigger = "bundle_change"
disabled = true
cmd = "sh -c 'exit 8'"
[[schedule]]
name = "cron-only"
cron = "0 * * * *"
cmd = "sh -c 'exit 9'"
[[schedule]]
name = "producer"
cron = "0 * * * *"
deploy_trigger = "first_deploy"
cmd = "sh -c 'exit 7'"
[[schedule]]
name = "later"
cron = "0 * * * *"
deploy_trigger = "bundle_change"
cmd = "sh -c 'exit 6'"
`)
	var out bytes.Buffer
	err := Run(context.Background(), Options{BundleDir: dir, StateDir: t.TempDir(), Seed: true, NoSync: true}, &out, io.Discard)
	var jobErr *ScheduleError
	if !errors.As(err, &jobErr) || jobErr.Code != 7 {
		t.Fatalf("error=%v", err)
	}
	if strings.Contains(out.String(), "starting") || strings.Contains(out.String(), "==> schedule later") {
		t.Fatalf("continued after failure: %s", &out)
	}
}

func TestSeedIsOptInAndRunsAgainOnEachInvocation(t *testing.T) {
	dir := writeHealthyFixture(t)
	path := filepath.Join(dir, "shinyhub.toml")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`
[[schedule]]
name = "producer"
cron = "0 * * * *"
deploy_trigger = "bundle_change"
cmd = "sh -c 'echo seeded >> $SHINYHUB_APP_DATA/count'"
`)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	for _, seed := range []bool{false, true, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := Run(ctx, Options{BundleDir: dir, StateDir: state, Seed: seed, NoSync: true, Check: true, NoReload: true}, io.Discard, io.Discard)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if !seed {
			if _, err := os.Stat(filepath.Join(state, "data", "count")); !os.IsNotExist(err) {
				t.Fatal("default start ran producer")
			}
		}
	}
	got, err := os.ReadFile(filepath.Join(state, "data", "count"))
	if err != nil || string(got) != "seeded\nseeded\n" {
		t.Fatalf("count=%q error=%v", got, err)
	}
}

func TestDataLockExcludesWritersAcrossWorkspaces(t *testing.T) {
	data := t.TempDir()
	consumer, err := acquireDataLock(data, false)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	second, err := acquireDataLock(data, false)
	if err != nil {
		t.Fatal(err)
	}
	second.Close()
	dir := scheduleFixture(t, `
[[schedule]]
name = "fetch"
cron = "0 * * * *"
cmd = "true"
`)
	err = Run(context.Background(), Options{BundleDir: dir, StateDir: t.TempDir(), DataDir: data, ScheduleName: "fetch", NoSync: true}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "data is in use") {
		t.Fatalf("writer admitted: %v", err)
	}
	consumer.Close()
	producer, err := acquireDataLock(data, true)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	if f, err := acquireDataLock(data, false); err == nil {
		f.Close()
		t.Fatal("consumer admitted during production")
	}
}

func TestSeedDoesNotRunOnReload(t *testing.T) {
	dir := writeHealthyFixture(t)
	path := filepath.Join(dir, "shinyhub.toml")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`
[[schedule]]
name = "producer"
cron = "0 * * * *"
deploy_trigger = "bundle_change"
cmd = "sh -c 'echo seeded >> $SHINYHUB_APP_DATA/count'"
`)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	version := filepath.Join(dir, "version.txt")
	if err := os.WriteFile(version, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	port := deploy.AllocatePort()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{BundleDir: dir, StateDir: state, Slug: "fixture", Port: port, Seed: true, NoSync: true}, io.Discard, io.Discard)
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("runner did not stop")
		}
	}()
	url := fmt.Sprintf("http://127.0.0.1:%d/app/fixture/version.txt", port)
	waitForBody(t, url, "v1", 5*time.Second)
	if err := os.WriteFile(version, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForBody(t, url, "v2", 6*time.Second)
	got, err := os.ReadFile(filepath.Join(state, "data", "count"))
	if err != nil || string(got) != "seeded\n" {
		t.Fatalf("reload ran producer: count=%q error=%v", got, err)
	}
}

func TestLocalScheduleCancellationStopsDescendants(t *testing.T) {
	dir := scheduleFixture(t, `
[[schedule]]
name = "fetch"
cron = "0 * * * *"
cmd = "sh producer.sh"
`)
	script := `sleep 1
printf leaked > "$SHINYHUB_APP_DATA/leak"
`
	if err := os.WriteFile(filepath.Join(dir, "producer.sh"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	state := t.TempDir()
	err := Run(ctx, Options{BundleDir: dir, StateDir: state, ScheduleName: "fetch", NoSync: true}, io.Discard, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(state, "data", "leak")); !os.IsNotExist(err) {
		t.Fatal("descendant survived cancellation")
	}
}

func TestDataLockSurvivesParentCloseWhileChildRuns(t *testing.T) {
	data := t.TempDir()
	lock, err := acquireDataLock(data, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	child := exec.Command("sleep", "30")
	child.ExtraFiles = []*os.File{lock}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	lock.Close()
	if f, err := acquireDataLock(data, true); err == nil {
		f.Close()
		t.Fatal("writer admitted while inherited consumer fence survives")
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	writer, err := acquireDataLock(data, true)
	if err != nil {
		t.Fatal(err)
	}
	writer.Close()
}

func TestDataLockSurvivesDataDirectoryReplacement(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(data, 0o755); err != nil {
		t.Fatal(err)
	}
	held, err := acquireDataLock(data, true)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := os.RemoveAll(data); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(data, 0o755); err != nil {
		t.Fatal(err)
	}
	if f, err := acquireDataLock(data, true); err == nil {
		f.Close()
		t.Fatal("replacement data directory lost its producer fence")
	}
	if f, err := acquireDataLock(data, false); err == nil {
		f.Close()
		t.Fatal("consumer admitted during a fenced refresh")
	}
}

func TestFreshDoesNotResetWorkspaceBeforeDataAdmission(t *testing.T) {
	dir := scheduleFixture(t, "")
	state := t.TempDir()
	mirrored := filepath.Join(state, "bundles", "0")
	if err := os.MkdirAll(mirrored, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(mirrored, "still-in-use")
	if err := os.WriteFile(sentinel, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	held, err := acquireDataLock(filepath.Join(state, "data"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	err = Run(context.Background(), Options{BundleDir: dir, StateDir: state, Fresh: true, NoSync: true}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "data is in use") {
		t.Fatalf("error=%v", err)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "original" {
		t.Fatalf("workspace modified before admission: %q %v", got, err)
	}
}

func TestWorkspaceLockSurvivesParentClose(t *testing.T) {
	w := &workspace{Root: t.TempDir()}
	release, err := w.acquireLock()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	child := exec.Command("sleep", "30")
	child.ExtraFiles = []*os.File{w.workspaceLock}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	release()
	if unlock, err := (&workspace{Root: w.Root}).acquireLock(); err == nil {
		unlock()
		t.Fatal("orphan workspace was admitted")
	}
	child.Process.Kill()
	child.Wait()
	unlock, err := (&workspace{Root: w.Root}).acquireLock()
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestStopChildKillsSIGTERMResistantDescendant(t *testing.T) {
	data := t.TempDir()
	lock, err := acquireDataLock(data, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command("sh", "-c", `trap 'exit 0' TERM
(trap '' TERM; echo ready > "$1"; while :; do sleep 1; done) >/dev/null 2>&1 &
wait
`, "fixture", ready)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.ExtraFiles = []*os.File{lock}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := watchExit(cmd)
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("descendant did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	lock.Close()
	stopChild(cmd, done, io.Discard)
	deadline = time.Now().Add(3 * time.Second)
	for {
		if writer, err := acquireDataLock(data, true); err == nil {
			writer.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("surviving descendant retained its fence after shutdown")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLocalScheduleRejectsIncompleteBackgroundProducer(t *testing.T) {
	dir := scheduleFixture(t, `
[[schedule]]
name = "fetch"
cron = "0 * * * *"
cmd = "sh -c 'sleep 30 & exit 0'"
`)
	err := Run(context.Background(), Options{BundleDir: dir, StateDir: t.TempDir(), ScheduleName: "fetch", NoSync: true}, io.Discard, io.Discard)
	var scheduleErr *ScheduleError
	if !errors.As(err, &scheduleErr) || scheduleErr.Code != 1 || !errors.Is(err, exec.ErrWaitDelay) || !strings.Contains(err.Error(), "foreground") {
		t.Fatalf("error=%v", err)
	}
}

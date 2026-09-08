package process

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
	"sync"
	"syscall"
	"testing"
	"time"
)

// A launcher that forks a long-lived child and then waits, the shape uv/Rscript
// have: the child is what serves traffic, and the leader is all exec.Cmd sees.
const orphanLauncher = `#!/bin/sh
"$1" &
echo $! > "$2"
wait
`

// A child that ignores nothing and outlives its parent unless something kills
// the group. It writes its own liveness so the test never has to guess.
const orphanChild = `#!/bin/sh
while true; do sleep 0.1; done
`

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// The other bound on the pipe grace: an ordinary exit closes the pipe itself,
// so Wait must return promptly with the whole log rather than paying the grace
// period or truncating output the app had already written.
func TestNativeWaitKeepsTheWholeLogOnAnOrdinaryExit(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "chatty.sh", "#!/bin/sh\ni=0\nwhile [ $i -lt 500 ]; do echo \"line $i\"; i=$((i+1)); done\nexit 3\n")

	var log lockedBuffer
	rt := NewNativeRuntime()
	ep, err := rt.Start(context.Background(), StartParams{
		Slug: "chatty", Dir: dir, Command: []string{script},
	}, &log)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	start := time.Now()
	err = rt.Wait(context.Background(), ep.Handle)
	elapsed := time.Since(start)

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("Wait must report the real exit status, got %v", err)
	}
	if elapsed >= leaderExitPipeGrace {
		t.Fatalf("an ordinary exit waited out the pipe grace (%s); the grace must only apply when something still holds the pipe", elapsed)
	}
	if got := strings.Count(log.String(), "line "); got != 500 {
		t.Fatalf("log lost output: %d of 500 lines", got)
	}
}

// lockedBuffer is a log sink safe for the goroutine os/exec copies output on.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestNativeWaitReapsTheGroupWhenTheLeaderIsKilledDirectly is the OR-F1
// reproduction: a SIGKILL aimed at the recorded leader, not at the child that
// actually serves. If Wait does not return, the manager's exit monitor never
// records the crash and the child keeps serving on its port with the replica
// still reported healthy - supervision silently stops applying to it.
func TestNativeWaitReapsTheGroupWhenTheLeaderIsKilledDirectly(t *testing.T) {
	dir := t.TempDir()
	child := writeScript(t, dir, "child.sh", orphanChild)
	launcher := writeScript(t, dir, "launcher.sh", orphanLauncher)
	childPIDFile := filepath.Join(dir, "child.pid")

	rt := NewNativeRuntime()
	ep, err := rt.Start(context.Background(), StartParams{
		Slug:    "orphan",
		Dir:     dir,
		Command: []string{launcher, child, childPIDFile},
		Port:    0,
	}, io.Discard)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	leader := ep.Handle.PID

	var childPID int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(childPIDFile)
		if err == nil && len(b) > 1 {
			if _, err := fmt.Sscan(string(b), &childPID); err == nil && childPID > 0 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("the launcher never reported its child; the reproduction never got set up")
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-leader, syscall.SIGKILL)
		_ = syscall.Kill(childPID, syscall.SIGKILL)
	})
	if !alive(childPID) {
		t.Fatal("child is not running, so the test cannot observe it being orphaned")
	}

	waitErr := make(chan error, 1)
	go func() { waitErr <- rt.Wait(context.Background(), ep.Handle) }()

	// Kill the leader only. The child is untouched and is in the same group.
	if err := syscall.Kill(leader, syscall.SIGKILL); err != nil {
		t.Fatalf("kill leader: %v", err)
	}

	var err2 error
	select {
	case err2 = <-waitErr:
	case <-time.After(30 * time.Second):
		t.Fatal("Wait never returned after the leader was killed: the manager never learns the replica exited, so no crash is recorded and the orphan keeps serving")
	}
	// The manager builds its crash verdict from this error, so bounding the
	// pipe wait must not replace the signal that explains the exit. ErrWaitDelay
	// would erase "killed" and the replica's recorded cause with it.
	var exitErr *exec.ExitError
	if !errors.As(err2, &exitErr) {
		t.Fatalf("Wait must still report how the leader died, got %v", err2)
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); !ok || status.Signal() != syscall.SIGKILL {
		t.Fatalf("exit verdict lost the signal that caused it: %v", err2)
	}

	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		if !alive(childPID) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("child %d survived the leader's death: it keeps serving on the replica's port with nothing supervising it", childPID)
}

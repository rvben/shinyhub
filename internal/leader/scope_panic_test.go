package leader

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/safego"
)

// TestOwnerScope_RecoversWorkPanic proves a panic in owner-only work is
// recovered rather than crashing the whole process (an unrecovered goroutine
// panic is fatal). If recovery were missing, this test binary would abort
// instead of Lose() returning cleanly (PROD-1).
func TestOwnerScope_RecoversWorkPanic(t *testing.T) {
	s := NewOwnerScope(func(ctx context.Context, epoch int64) {
		panic("boom")
	})
	s.Acquire(1)
	s.Lose() // blocks until the (recovered) work goroutine finishes
}

// TestOwnerScope_FatalPanicPropagates proves a safego.Fatal raised in owner
// work is not absorbed by the scope's recover. It re-panics on the work
// goroutine and ends the process, so the case runs in a child process.
func TestOwnerScope_FatalPanicPropagates(t *testing.T) {
	if os.Getenv("FATAL_PANIC_TEST_CHILD") == "1" {
		s := NewOwnerScope(func(ctx context.Context, epoch int64) {
			panic(safego.Fatal{Value: "owner-fatal-marker"})
		})
		s.Acquire(1)
		s.Lose()
		return // reaching here means the Fatal was absorbed
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestOwnerScope_FatalPanicPropagates$", "-test.count=1")
	cmd.Env = append(os.Environ(), "FATAL_PANIC_TEST_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("child process survived a safego.Fatal raised in owner work; output:\n%s", out)
	}
	if !strings.Contains(string(out), "panic: (safego.Fatal)") {
		t.Fatalf("child exited (%v) without dying of the safego.Fatal:\n%s", err, out)
	}
}

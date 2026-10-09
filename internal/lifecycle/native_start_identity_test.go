//go:build unix

package lifecycle

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
)

func TestRecordedNativeStopNeverSignalsReusedPID(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	pid := cmd.Process.Pid
	started, err := process.NativeProcessStartIdentity(pid)
	if err != nil {
		t.Fatal(err)
	}
	deploymentID := int64(1)
	progress := nativeStopProgress{}
	result := stepRecordedNativeStop(nil, &db.App{Slug: "reused"}, &pid, "native", &deploymentID, &progress, time.Millisecond, started+1)
	if result != nativeStopConfirmed {
		t.Fatalf("result=%v", result)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("unrelated live process was signalled: %v", err)
	}
	if !progress.termSentAt.IsZero() || !progress.killSentAt.IsZero() {
		t.Fatal("signal progress advanced for an unrelated process")
	}
}

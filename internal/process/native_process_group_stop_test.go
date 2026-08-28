//go:build unix

package process

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestNativeStopConfirmedWaitsForEntireProcessGroup(t *testing.T) {
	runtime := NewNativeRuntime()
	manager := NewManager(t.TempDir(), runtime)
	manager.SetStopGrace(100 * time.Millisecond)

	info, err := manager.Start(StartParams{
		Slug: "forking-consumer", Index: 0, Dir: t.TempDir(), Port: 19953,
		Command: []string{"/bin/sh", "-c", `trap 'exit 0' TERM; trap '' CHLD; /bin/sh -c 'trap "" TERM; while :; do sleep 1; done' & while :; do sleep 1; done`},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := manager.StopReplicaConfirmed("forking-consumer", 0); err != nil {
		t.Fatalf("confirmed stop: %v", err)
	}
	if err := syscall.Kill(-info.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("process group %d still exists after confirmed stop: %v", info.PID, err)
	}
}

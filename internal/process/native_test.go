package process_test

import (
	"bytes"
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

func TestNativeRuntimeStartStop(t *testing.T) {
	rt := process.NewNativeRuntime()
	var buf bytes.Buffer

	handle, err := rt.Start(context.Background(), process.StartParams{
		Slug:    "test",
		Dir:     t.TempDir(),
		Command: []string{"sleep", "10"},
		Port:    19100,
	}, &buf)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if handle.PID <= 0 {
		t.Fatalf("expected valid PID, got %d", handle.PID)
	}

	if err := rt.Signal(handle, syscall.SIGTERM); err != nil {
		t.Fatalf("Signal: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- rt.Wait(context.Background(), handle) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Wait timed out")
	}

	if err := syscall.Kill(handle.PID, 0); err == nil {
		t.Error("expected process to be dead after Signal+Wait")
	}
}

func TestNativeRuntimeEmptyCommand(t *testing.T) {
	rt := process.NewNativeRuntime()
	_, err := rt.Start(context.Background(), process.StartParams{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

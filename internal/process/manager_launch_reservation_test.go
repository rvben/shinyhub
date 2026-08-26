package process_test

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

func TestManagerLaunchReservationBlocksUnreservedStarts(t *testing.T) {
	rt := newFakeRuntime()
	m := process.NewManager(t.TempDir(), rt)
	release := m.AcquireLaunchReservation()

	done := make(chan error, 1)
	go func() {
		_, err := m.Start(process.StartParams{
			Slug: "other-app", Index: 0, Dir: t.TempDir(), Command: []string{"serve"}, Port: 19001,
		})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("start crossed held launch reservation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("start after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("start remained blocked after reservation release")
	}
	_ = m.Stop("other-app")
}

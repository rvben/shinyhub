package activation

import (
	"context"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/safego"
)

type fatalRunner struct{}

func (fatalRunner) Roll(context.Context, *db.ScheduleActivation) error {
	panic(safego.Fatal{Value: "roll-fatal"})
}

// The coordinator converts an ordinary runner panic into a repair-required
// outcome, but a safego.Fatal must keep unwinding so the process exits.
func TestCoordinatorDoesNotContainFatalRunnerPanic(t *testing.T) {
	store := &fakeStore{queue: []*db.ScheduleActivation{{ID: 7, AppSlug: "demo"}}}
	coordinator := New(store, fatalRunner{}, time.Second)
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = coordinator.ProcessNext(context.Background())
	}()
	if _, ok := recovered.(safego.Fatal); !ok {
		t.Fatalf("recovered %v (%T), want the safego.Fatal to propagate out of ProcessNext", recovered, recovered)
	}
}

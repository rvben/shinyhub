package process

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A stop that picked a launching Start out by its generation must cancel
// only that Start. If the captured Start has finished and a later Start has
// reserved the same slot, the later one is left launching.
func TestCancelPendingStartLeavesALaterStartAlone(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	m.SetStopGrace(10 * time.Millisecond)
	key := replicaKey{Slug: "demo", Index: 0}
	captured := &pendingStart{key: key, slug: "demo", deploymentID: 101, done: make(chan struct{})}
	close(captured.done)
	later := &pendingStart{key: key, slug: "demo", deploymentID: 202, done: make(chan struct{})}
	m.mu.Lock()
	m.starting[key] = later
	m.mu.Unlock()

	proceed, err := m.cancelPendingStart("demo", key, captured, true)
	if err != nil {
		t.Fatalf("cancel of a Start that already finished: %v", err)
	}
	if !proceed {
		t.Error("the caller must go on to inspect the slot the captured Start left")
	}
	m.mu.Lock()
	cancelled := later.cancelled
	m.mu.Unlock()
	if cancelled {
		t.Fatal("a stop for deployment 101 cancelled the launch of deployment 202")
	}

	// Without a captured Start, whatever is launching in the slot is the target.
	if _, err := m.cancelPendingStart("demo", key, nil, false); err != nil {
		t.Fatalf("slot-wide cancel: %v", err)
	}
	m.mu.Lock()
	cancelled = later.cancelled
	m.mu.Unlock()
	if !cancelled {
		t.Fatal("a slot-wide stop did not cancel the launch in the slot")
	}
}

// Replicas of one bundle provision its data link concurrently. Every one of
// them must accept the link another has just created, and none may fail.
func TestLinkBundleDataConcurrentStartsAllSucceed(t *testing.T) {
	target := t.TempDir()
	for round := 0; round < 50; round++ {
		link := filepath.Join(t.TempDir(), "data")
		const replicas = 8
		var wg sync.WaitGroup
		gate := make(chan struct{})
		errs := make(chan error, replicas)
		for i := 0; i < replicas; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-gate
				errs <- linkBundleData("demo", link, target)
			}()
		}
		close(gate)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: concurrent data link: %v", round, err)
			}
		}
		if got, err := os.Readlink(link); err != nil || got != target {
			t.Fatalf("round %d: data link = %q, %v; want %q", round, got, err, target)
		}
	}
}

// Anything at <bundle>/data that is not the app's data link is refused, also
// when it appears between the check and the create.
func TestLinkBundleDataRefusesAForeignEntry(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "data")
	if err := os.Mkdir(link, 0o750); err != nil {
		t.Fatal(err)
	}
	err := linkBundleData("demo", link, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "reserves that path") {
		t.Fatalf("a bundle directory named data: got %v, want it refused", err)
	}
	other := filepath.Join(dir, "other")
	if err := os.Symlink(t.TempDir(), other); err != nil {
		t.Fatal(err)
	}
	if err := linkBundleData("demo", other, t.TempDir()); err == nil {
		t.Fatal("a data link to another directory was accepted")
	}
}

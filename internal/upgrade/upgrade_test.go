package upgrade

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

var errTest = errors.New("upgrade boom")

// fakeUpgrader records Upgrade/Stop calls and lets a test close Exit.
type fakeUpgrader struct {
	mu         sync.Mutex
	upgrades   int
	upgradeErr error
	stopped    bool
	exitCh     chan struct{}
}

func newFakeUpgrader() *fakeUpgrader { return &fakeUpgrader{exitCh: make(chan struct{})} }

func (f *fakeUpgrader) Listen(network, addr string) (net.Listener, error) { return nil, nil }
func (f *fakeUpgrader) Ready() error                                      { return nil }
func (f *fakeUpgrader) Exit() <-chan struct{}                             { return f.exitCh }

func (f *fakeUpgrader) Upgrade() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upgrades++
	return f.upgradeErr
}

func (f *fakeUpgrader) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.stopped {
		f.stopped = true
		close(f.exitCh)
	}
}

func (f *fakeUpgrader) upgradeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.upgrades
}

func (f *fakeUpgrader) isStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// waitFor polls cond up to 2s; fails the test otherwise.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", msg)
}

func TestWireSignals_SIGHUPTriggersUpgrade(t *testing.T) {
	f := newFakeUpgrader()
	sighup := make(chan os.Signal, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	WireSignals(ctx, f, sighup, quietLogger())

	sighup <- syscall.SIGHUP
	waitFor(t, func() bool { return f.upgradeCount() == 1 }, "one Upgrade call")
	if f.isStopped() {
		t.Fatal("a successful SIGHUP upgrade must not Stop the upgrader")
	}
}

func TestWireSignals_UpgradeErrorKeepsServing(t *testing.T) {
	f := newFakeUpgrader()
	f.upgradeErr = errTest
	sighup := make(chan os.Signal, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	WireSignals(ctx, f, sighup, quietLogger())

	sighup <- syscall.SIGHUP
	waitFor(t, func() bool { return f.upgradeCount() == 1 }, "one Upgrade attempt")
	// A failed upgrade must not stop the process and must not close Exit.
	time.Sleep(50 * time.Millisecond)
	if f.isStopped() {
		t.Fatal("a failed upgrade must not Stop the upgrader")
	}
	select {
	case <-f.Exit():
		t.Fatal("Exit must not close on a failed upgrade")
	default:
	}
}

func TestWireSignals_ContextCancelStops(t *testing.T) {
	f := newFakeUpgrader()
	sighup := make(chan os.Signal, 1)
	ctx, cancel := context.WithCancel(context.Background())

	WireSignals(ctx, f, sighup, quietLogger())

	cancel()
	waitFor(t, func() bool { return f.isStopped() }, "Stop after ctx cancel")
	select {
	case <-f.Exit():
	default:
		t.Fatal("Stop must close Exit")
	}
}

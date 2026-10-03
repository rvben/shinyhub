package proxy

import (
	"net"
	"testing"
	"time"
)

// The session-expiry deadline closes a tracked connection on a timer
// goroutine, running the connection's close hook. A panic there must be
// contained rather than crash the server.
func TestConnTracker_DeadlineClosePanicIsContained(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	fired := make(chan struct{})
	tracker := newConnTracker()
	tracker.trackWithClose(server, ConnPrincipal{SessionExpiresAt: time.Now().Add(20 * time.Millisecond)}, func() {
		close(fired)
		panic("close hook fault")
	})
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("deadline close never ran")
	}
	// Give an uncontained panic time to take the test binary down.
	time.Sleep(100 * time.Millisecond)
	if n := tracker.count(); n != 0 {
		t.Fatalf("tracked connections = %d, want 0 (the deadline close unregisters before the hook)", n)
	}
}

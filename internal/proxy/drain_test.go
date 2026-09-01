package proxy

import (
	"net"
	"sync"
	"testing"
	"time"
)

// stubConn is a no-op net.Conn whose Close records that it happened.
type stubConn struct {
	net.Conn
	mu     sync.Mutex
	closed bool
}

func (c *stubConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (c *stubConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func TestConnTracker_TrackCountForget(t *testing.T) {
	tr := newConnTracker()
	if tr.count() != 0 {
		t.Fatalf("empty tracker count = %d, want 0", tr.count())
	}
	a := tr.track(&stubConn{}, ConnPrincipal{}).(*trackedConn)
	b := tr.track(&stubConn{}, ConnPrincipal{})
	if tr.count() != 2 {
		t.Fatalf("count after 2 tracks = %d, want 2", tr.count())
	}
	a.Close()
	a.Close() // idempotent: must not double-decrement
	if tr.count() != 1 {
		t.Fatalf("count after closing one = %d, want 1", tr.count())
	}
	b.Close()
	if tr.count() != 0 {
		t.Fatalf("count after closing both = %d, want 0", tr.count())
	}
}

func TestConnTracker_OnCloseRunsExactlyOnce(t *testing.T) {
	tr := newConnTracker()
	closed := 0
	conn := tr.trackWithClose(&stubConn{}, ConnPrincipal{}, func() { closed++ })
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if closed != 1 {
		t.Fatalf("onClose calls = %d, want 1", closed)
	}
}

func TestConnTracker_SupportDeadlineClosesWithoutRecheckSweep(t *testing.T) {
	tr := newConnTracker()
	underlying := &stubConn{}
	tr.track(underlying, ConnPrincipal{SupportExpiresAt: time.Now().Add(30 * time.Millisecond)})
	deadline := time.After(time.Second)
	for !underlying.isClosed() {
		select {
		case <-deadline:
			t.Fatal("support connection survived its hard deadline")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if tr.count() != 0 {
		t.Fatalf("expired support connection remains tracked: %d", tr.count())
	}
}

func TestConnTracker_EarlyCloseCancelsSupportDeadlineTimer(t *testing.T) {
	tr := newConnTracker()
	tc := tr.track(&stubConn{}, ConnPrincipal{SupportExpiresAt: time.Now().Add(15 * time.Minute)}).(*trackedConn)
	if err := tc.Close(); err != nil {
		t.Fatal(err)
	}
	tc.timerMu.Lock()
	defer tc.timerMu.Unlock()
	if tc.deadlineTimer != nil {
		t.Fatal("closed support connection retained its deadline timer")
	}
}

func TestConnTracker_CloseAllForceClosesUnderlying(t *testing.T) {
	tr := newConnTracker()
	s1, s2 := &stubConn{}, &stubConn{}
	tr.track(s1, ConnPrincipal{})
	tr.track(s2, ConnPrincipal{})
	forced := tr.closeAll()
	if forced != 2 {
		t.Fatalf("closeAll returned %d, want 2", forced)
	}
	if !s1.isClosed() || !s2.isClosed() {
		t.Fatal("closeAll must close the underlying connections")
	}
	if tr.count() != 0 {
		t.Fatalf("count after closeAll = %d, want 0", tr.count())
	}
}

func TestProxy_DrainingFlag(t *testing.T) {
	p := New()
	if p.Draining() {
		t.Fatal("a fresh proxy must not be draining")
	}
	p.SetDraining(true)
	if !p.Draining() {
		t.Fatal("SetDraining(true) must set the flag")
	}
}

func TestProxy_DrainUpgraded_NoneReturnsImmediately(t *testing.T) {
	p := New()
	start := time.Now()
	if forced := p.DrainUpgraded(5 * time.Second); forced != 0 {
		t.Fatalf("DrainUpgraded with no conns forced=%d, want 0", forced)
	}
	if time.Since(start) > time.Second {
		t.Fatal("DrainUpgraded must return immediately when no conns are open")
	}
}

func TestProxy_DrainUpgraded_WaitsThenForceCloses(t *testing.T) {
	p := New()
	s := &stubConn{}
	p.conns.track(s, ConnPrincipal{})
	start := time.Now()
	forced := p.DrainUpgraded(150 * time.Millisecond)
	if forced != 1 {
		t.Fatalf("forced = %d, want 1", forced)
	}
	if !s.isClosed() {
		t.Fatal("straggler must be force-closed at the deadline")
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("DrainUpgraded must wait at least one poll interval before force-closing")
	}
}

func TestProxy_DrainUpgraded_ReturnsEarlyWhenConnsClose(t *testing.T) {
	p := New()
	tc := p.conns.track(&stubConn{}, ConnPrincipal{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		tc.Close()
	}()
	start := time.Now()
	forced := p.DrainUpgraded(5 * time.Second)
	if forced != 0 {
		t.Fatalf("forced = %d, want 0 (conn closed on its own)", forced)
	}
	if time.Since(start) > time.Second {
		t.Fatal("DrainUpgraded must return promptly once conns drain")
	}
}

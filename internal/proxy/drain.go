package proxy

import (
	"net"
	"sync"
	"time"
)

// connTracker is the registry of live hijacked (upgraded / WebSocket)
// connections served by this instance. A graceful shutdown uses it to wait for
// those connections to close, count them, and force-close any stragglers at the
// drain deadline. It is separate from the per-replica activeConns request
// counter (which serves hibernation) - this set tracks individual long-lived
// connections, which activeConns cannot.
type connTracker struct {
	mu    sync.Mutex
	conns map[*trackedConn]struct{}
}

func newConnTracker() *connTracker {
	return &connTracker{conns: make(map[*trackedConn]struct{})}
}

// track wraps c so that closing it unregisters it, registers the wrapper, and
// returns the wrapper to hand back to the hijacking caller.
func (t *connTracker) track(c net.Conn) net.Conn {
	tc := &trackedConn{Conn: c, tracker: t}
	t.mu.Lock()
	t.conns[tc] = struct{}{}
	t.mu.Unlock()
	return tc
}

func (t *connTracker) forget(tc *trackedConn) {
	t.mu.Lock()
	delete(t.conns, tc)
	t.mu.Unlock()
}

func (t *connTracker) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.conns)
}

// closeAll force-closes every tracked connection and returns how many were
// open. Each Close unregisters its own entry (via trackedConn.Close), so the
// set is empty afterward.
func (t *connTracker) closeAll() int {
	t.mu.Lock()
	open := make([]*trackedConn, 0, len(t.conns))
	for tc := range t.conns {
		open = append(open, tc)
	}
	t.mu.Unlock()
	for _, tc := range open {
		tc.Close()
	}
	return len(open)
}

// trackedConn wraps a hijacked net.Conn so the first Close unregisters it from
// the tracker. All other net.Conn behaviour is inherited unchanged. Close is
// fully idempotent: the underlying conn is closed exactly once and the cached
// error is returned on every subsequent call.
type trackedConn struct {
	net.Conn
	tracker  *connTracker
	once     sync.Once
	closeErr error
}

func (c *trackedConn) Close() error {
	c.once.Do(func() {
		c.tracker.forget(c)
		c.closeErr = c.Conn.Close()
	})
	return c.closeErr
}

// SetDraining marks (or unmarks) this instance as draining for shutdown. While
// draining, /readyz reports unready so a load balancer stops routing new
// requests; existing hijacked connections keep flowing.
func (p *Proxy) SetDraining(v bool) { p.instanceDraining.Store(v) }

// Draining reports whether this instance is draining for shutdown.
func (p *Proxy) Draining() bool { return p.instanceDraining.Load() }

// ActiveUpgradedConns is the number of live hijacked (WebSocket) connections.
func (p *Proxy) ActiveUpgradedConns() int { return p.conns.count() }

// DrainUpgraded waits for all tracked upgraded connections to close on their
// own, up to timeout, then force-closes any that remain. It returns the number
// force-closed (0 means everything drained cleanly). It returns immediately
// when no upgraded connections are open. Callers are expected to have called
// SetDraining(true) and stopped accepting new connections before calling
// DrainUpgraded, so the tracked set only shrinks.
func (p *Proxy) DrainUpgraded(timeout time.Duration) (forced int) {
	if p.conns.count() == 0 {
		return 0
	}
	deadline := time.Now().Add(timeout)
	for p.conns.count() > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	return p.conns.closeAll()
}

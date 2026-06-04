// Package upgrade wraps github.com/cloudflare/tableflip behind a small
// interface so ShinyHub can perform zero-downtime, listener-preserving restarts
// (SIGHUP handoff) and unit-test the wiring with a fake. *tableflip.Upgrader
// satisfies Upgrader directly.
package upgrade

import (
	"net"
	"time"

	"github.com/cloudflare/tableflip"
)

// Upgrader is the subset of *tableflip.Upgrader the server uses.
type Upgrader interface {
	// Listen returns a listener whose underlying socket survives an upgrade
	// (inherited by the successor process). All Listen calls must precede Ready.
	Listen(network, addr string) (net.Listener, error)
	// Ready signals this process is serving: it closes inherited-but-unused
	// fds, writes the PID file, and tells the parent (if this is an upgrade
	// child) that it may exit. Call once after every Listen and after the
	// servers are accepting.
	Ready() error
	// Upgrade spawns the new binary, hands it the listener fds, and blocks until
	// it calls Ready (or UpgradeTimeout elapses). On success the caller should
	// shut down; on error the caller keeps serving.
	Upgrade() error
	// Exit is closed when this process should shut down: after Stop, or after a
	// successful Upgrade handed off to a ready successor.
	Exit() <-chan struct{}
	// Stop disables further upgrades and closes Exit.
	Stop()
}

// New returns a tableflip-backed Upgrader. timeout bounds the wait for a
// successor to become Ready during an upgrade; pidFile, when non-empty, receives
// the ready process's PID (systemd MAINPID tracking).
func New(timeout time.Duration, pidFile string) (Upgrader, error) {
	return tableflip.New(tableflip.Options{
		UpgradeTimeout: timeout,
		PIDFile:        pidFile,
	})
}

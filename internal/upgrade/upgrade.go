// Package upgrade wraps github.com/cloudflare/tableflip behind a small
// interface so ShinyHub can perform zero-downtime, listener-preserving restarts
// (SIGHUP handoff) and unit-test the wiring with a fake. *tableflip.Upgrader
// satisfies Upgrader directly.
package upgrade

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
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

// WireSignals connects process signals to the upgrader lifecycle and returns
// immediately (it launches one goroutine per concern):
//   - each value on sighup triggers a zero-downtime Upgrade; a failed upgrade is
//     logged and the current process keeps serving.
//   - ctx cancellation (SIGINT/SIGTERM via signal.NotifyContext) calls Stop,
//     which closes Exit so the main loop proceeds to graceful shutdown.
func WireSignals(ctx context.Context, upg Upgrader, sighup <-chan os.Signal, log *slog.Logger) {
	// SIGHUP loop: triggers an upgrade per signal, and exits on ctx cancellation
	// so the goroutine does not outlive the server.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-sighup:
				if !ok {
					return
				}
				log.Info("SIGHUP received, starting zero-downtime upgrade")
				if err := upg.Upgrade(); err != nil {
					log.Error("zero-downtime upgrade failed; continuing to serve", "err", err)
					continue
				}
				log.Info("zero-downtime upgrade succeeded; successor is ready")
			}
		}
	}()
	// Dedicated Stop goroutine so a SIGINT/SIGTERM closes Exit promptly even if an
	// upgrade is in flight in the loop above.
	go func() {
		<-ctx.Done()
		upg.Stop()
	}()
}

// NotifyReady tells systemd (Type=notify) that this process is the live service:
// it sends READY=1 and MAINPID=<own pid> to $NOTIFY_SOCKET so systemd retargets
// MAINPID to the successor after every zero-downtime handoff. It is a no-op when
// $NOTIFY_SOCKET is unset (dev, macOS, non-systemd). Call once, right after
// Upgrader.Ready().
func NotifyReady() error {
	sock := os.Getenv("NOTIFY_SOCKET")
	if sock == "" {
		return nil
	}
	// Abstract namespace sockets are encoded with a leading '@' in NOTIFY_SOCKET.
	if strings.HasPrefix(sock, "@") {
		sock = "\x00" + sock[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		return fmt.Errorf("sd_notify dial: %w", err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "READY=1\nMAINPID=%d\n", os.Getpid()); err != nil {
		return fmt.Errorf("sd_notify write: %w", err)
	}
	return nil
}

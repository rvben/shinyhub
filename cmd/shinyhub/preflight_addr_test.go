package main

import (
	"net"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/upgrade"
)

// stubUpgrader is the smallest thing satisfying upgrade.Upgrader for the
// preflight, which uses only HasParent.
type stubUpgrader struct{ hasParent bool }

func (s stubUpgrader) Listen(string, string) (net.Listener, error) { return nil, nil }
func (s stubUpgrader) Ready() error                                { return nil }
func (s stubUpgrader) Upgrade() error                              { return nil }
func (s stubUpgrader) Exit() <-chan struct{}                       { return nil }
func (s stubUpgrader) Stop()                                       {}
func (s stubUpgrader) HasParent() bool                             { return s.hasParent }

var _ upgrade.Upgrader = stubUpgrader{}

// takenAddr binds a loopback port and keeps it bound for the test.
func takenAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind fixture listener: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}

// freeAddr returns an address that was bindable and is now free again. There is
// no way to reserve one, so this is the same racy-in-principle probe the
// preflight itself performs; on a loopback ephemeral port it is stable.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind fixture listener: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close fixture listener: %v", err)
	}
	return addr
}

// TestPreflightAddrFree_RejectsATakenAddress is the whole point: a second
// instance on an occupied port must be told so before startup does any work.
// The message has to name the address, because an operator reading it is
// deciding whether the conflict is their own other instance or something else.
func TestPreflightAddrFree_RejectsATakenAddress(t *testing.T) {
	addr := takenAddr(t)
	err := preflightAddrFree(stubUpgrader{}, "server.port", addr)
	if err == nil {
		t.Fatal("expected an error for an address that is already bound")
	}
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("error %q does not name the address that is taken", err)
	}
	if !strings.Contains(err.Error(), "server.port") {
		t.Errorf("error %q does not name the config key to change", err)
	}
}

// TestPreflightAddrFree_AllowsAFreeAddress is the other bound. Without it the
// check above is satisfied by a preflight that refuses every address, which
// would make the server unstartable.
func TestPreflightAddrFree_AllowsAFreeAddress(t *testing.T) {
	if err := preflightAddrFree(stubUpgrader{}, "server.port", freeAddr(t)); err != nil {
		t.Errorf("free address rejected: %v", err)
	}
}

// TestPreflightAddrFree_ReleasesTheProbeSocket pins that the probe is not the
// binding. If it held the socket, the serving listener acquired later in
// startup would collide with this process's own preflight and no server could
// ever start.
func TestPreflightAddrFree_ReleasesTheProbeSocket(t *testing.T) {
	addr := freeAddr(t)
	if err := preflightAddrFree(stubUpgrader{}, "server.port", addr); err != nil {
		t.Fatalf("free address rejected: %v", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("address still bound after the preflight returned: %v", err)
	}
	_ = ln.Close()
}

// TestPreflightAddrFree_SkipsAnUpgradeChild covers the case that would break
// zero-downtime upgrades. The successor inherits the parent's listener, so the
// address is legitimately taken by the process it is replacing; probing it
// would abort every handoff.
func TestPreflightAddrFree_SkipsAnUpgradeChild(t *testing.T) {
	addr := takenAddr(t)
	if err := preflightAddrFree(stubUpgrader{hasParent: true}, "server.port", addr); err != nil {
		t.Errorf("an upgrade child was refused its inherited address: %v; every SIGHUP handoff would fail", err)
	}
}

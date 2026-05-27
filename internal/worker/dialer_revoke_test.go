package worker

import (
	"crypto/tls"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

// TestMTLSDialer_RefusesRevokedWorker asserts the control plane refuses to dial
// a revoked worker, so a control->worker connection is rejected immediately even
// while the worker's certificate is still within its TTL. This is defense in
// depth: routing already excludes revoked (down) workers, but a caller holding a
// stale db.Worker must not be able to reach a revoked node.
func TestMTLSDialer_RefusesRevokedWorker(t *testing.T) {
	mint := func() (tls.Certificate, error) {
		return selfSignedCert(t, time.Now().Add(-time.Minute), time.Now().Add(time.Hour)), nil
	}
	d, err := NewMTLSDialer(mint, nil)
	if err != nil {
		t.Fatalf("NewMTLSDialer: %v", err)
	}
	w := db.Worker{NodeID: "node-a", AdvertiseAddr: "10.0.0.5:8443", RevokedAt: "2026-01-01 00:00:00"}

	if _, _, err := d.DialWorker(w); err == nil {
		t.Fatal("DialWorker dialed a revoked worker")
	}
	if _, err := d.Transport(w); err == nil {
		t.Fatal("Transport returned a transport for a revoked worker")
	}
}

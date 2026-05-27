package worker

import (
	"crypto/x509"
	"testing"
)

// TestCAHolder_SetRotatesTrust verifies that the holder ignores a re-set of the
// same bundle, swaps trust when a different bundle arrives, and rejects an
// unparseable bundle without disturbing the current pool.
func TestCAHolder_SetRotatesTrust(t *testing.T) {
	ca1, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open ca1: %v", err)
	}
	ca2, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open ca2: %v", err)
	}

	h, err := NewCAHolder(ca1.CertPEM())
	if err != nil {
		t.Fatalf("new holder: %v", err)
	}

	if changed, err := h.Set(ca1.CertPEM()); err != nil || changed {
		t.Errorf("Set(same) = (changed=%v, err=%v), want (false, nil)", changed, err)
	}

	changed, err := h.Set(ca2.CertPEM())
	if err != nil {
		t.Fatalf("Set(ca2): %v", err)
	}
	if !changed {
		t.Fatal("Set(ca2) reported no change")
	}

	// The rotated pool must trust a ca2-signed cert and no longer build a chain
	// for a ca1-signed one.
	srv2, _ := ca2.ServerCertificate("127.0.0.1")
	leaf2, _ := x509.ParseCertificate(srv2.Certificate[0])
	if _, err := leaf2.Verify(x509.VerifyOptions{Roots: h.Pool()}); err != nil {
		t.Errorf("rotated pool rejects ca2 cert: %v", err)
	}
	srv1, _ := ca1.ServerCertificate("127.0.0.1")
	leaf1, _ := x509.ParseCertificate(srv1.Certificate[0])
	if _, err := leaf1.Verify(x509.VerifyOptions{Roots: h.Pool()}); err == nil {
		t.Error("rotated pool still trusts the superseded ca1 cert")
	}

	if _, err := h.Set([]byte("-----BEGIN CERTIFICATE-----\nnot base64\n-----END CERTIFICATE-----")); err == nil {
		t.Error("Set(invalid) did not error")
	}
	// The invalid Set must not have disturbed the ca2 pool.
	if _, err := leaf2.Verify(x509.VerifyOptions{Roots: h.Pool()}); err != nil {
		t.Errorf("pool changed after rejected Set: %v", err)
	}
}

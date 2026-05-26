package worker

import (
	"crypto/x509"
	"testing"
)

func TestControlClientCertificate_IsClientAuthAndVerifiable(t *testing.T) {
	ca, err := OpenCA(t.TempDir(), []string{"tok"})
	if err != nil {
		t.Fatalf("OpenCA: %v", err)
	}
	cert, err := ca.ControlClientCertificate()
	if err != nil {
		t.Fatalf("ControlClientCertificate: %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatal("no certificate bytes")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}

	// Must carry client-auth EKU.
	hasClientAuth := false
	for _, eku := range leaf.ExtKeyUsage {
		if eku == x509.ExtKeyUsageClientAuth {
			hasClientAuth = true
		}
	}
	if !hasClientAuth {
		t.Error("control client cert missing ClientAuth EKU")
	}

	// Must verify against the CA pool as a client cert.
	roots := ca.Pool()
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("control client cert does not verify against CA: %v", err)
	}
}

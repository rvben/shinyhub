// internal/worker/ca_test.go
package worker

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"testing"
	"time"
)

func TestCAServerCertificateVerifies(t *testing.T) {
	ca, err := OpenCA(t.TempDir(), []string{"t"})
	if err != nil {
		t.Fatalf("open ca: %v", err)
	}
	cert, err := ca.ServerCertificate()
	if err != nil {
		t.Fatalf("server cert: %v", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     ca.Pool(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("server cert does not verify against CA: %v", err)
	}
}

func TestCASignsCSRWithNodeIDBinding(t *testing.T) {
	dir := t.TempDir()
	ca, err := OpenCA(dir, []string{"join-secret"})
	if err != nil {
		t.Fatalf("open ca: %v", err)
	}
	if !ca.VerifyJoinToken("join-secret") {
		t.Fatal("valid token rejected")
	}
	if ca.VerifyJoinToken("wrong") {
		t.Fatal("invalid token accepted")
	}

	// Worker side: generate a key + CSR.
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "ignored-by-server"},
	}, key)
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	certPEM, err := ca.SignWorkerCSR("node-1", csrPEM, time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse signed cert: %v", err)
	}
	if NodeIDFromCert(cert) != "node-1" {
		t.Fatalf("node id binding = %q, want node-1", NodeIDFromCert(cert))
	}

	// The signed cert must verify against the CA pool.
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     ca.Pool(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("verify against CA pool: %v", err)
	}
}

func TestCAPersistsKeypairAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	ca1, err := OpenCA(dir, []string{"t"})
	if err != nil {
		t.Fatalf("open1: %v", err)
	}
	ca2, err := OpenCA(dir, []string{"t"})
	if err != nil {
		t.Fatalf("open2: %v", err)
	}
	if string(ca1.CertPEM()) != string(ca2.CertPEM()) {
		t.Fatal("CA cert regenerated on second open; must persist")
	}
}

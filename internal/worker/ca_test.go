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

func TestLoadCA_RejectsMismatchedKey(t *testing.T) {
	// A valid CA cert paired with an unrelated private key must be rejected.
	certPEM, _ := generateCA() // helper added in this task
	_, otherKeyPEM := mustGenerateOtherECKey(t)
	if _, err := loadCA(certPEM, otherKeyPEM, nil); err == nil {
		t.Fatal("loadCA accepted a cert/key whose public keys differ")
	}
}

func TestLoadCA_RejectsTamperedCert(t *testing.T) {
	certPEM, keyPEM := generateCA()
	tampered := flipOneCertByte(t, certPEM) // helper: flip a byte inside the DER
	if _, err := loadCA(tampered, keyPEM, nil); err == nil {
		t.Fatal("loadCA accepted a cert with a broken self-signature")
	}
}

func TestLoadCA_AcceptsValidPair(t *testing.T) {
	certPEM, keyPEM := generateCA()
	ca, err := loadCA(certPEM, keyPEM, nil)
	if err != nil {
		t.Fatalf("valid pair rejected: %v", err)
	}
	if ca == nil || len(ca.CertPEM()) == 0 {
		t.Fatal("loaded CA empty")
	}
}

// mustGenerateOtherECKey generates a fresh P-256 key unrelated to any cert,
// returning the PEM-encoded cert and key. The cert PEM is discarded by callers
// who only need an unrelated key.
func mustGenerateOtherECKey(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate other ec key: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		t.Fatalf("marshal other ec key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	// Also produce a self-signed cert for the other key so callers can use both.
	return nil, keyPEM
}

// flipOneCertByte decodes the PEM cert, flips a byte in the DER tail (away from
// the header), and re-encodes, producing a cert whose self-signature is invalid.
func flipOneCertByte(t *testing.T, certPEM []byte) []byte {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("flipOneCertByte: no PEM block")
	}
	der := make([]byte, len(block.Bytes))
	copy(der, block.Bytes)
	// Flip a byte near the end (inside the signature, not the header).
	der[len(der)-2] ^= 0xFF
	return pem.EncodeToMemory(&pem.Block{Type: block.Type, Bytes: der})
}

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

package worker

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"
)

// selfSignedCert builds a throwaway certificate with the given validity window.
// The rotating provider only inspects NotBefore/NotAfter, so a self-signed cert
// is sufficient to exercise its re-mint timing without a CA.
func selfSignedCert(t *testing.T, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func serialOf(t *testing.T, c *tls.Certificate) string {
	t.Helper()
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return leaf.SerialNumber.String()
}

// TestRotatingClientCert_ReMintsWhenPastHalfLife verifies the control plane's
// client cert provider mints once eagerly, reuses the cert while it is fresh,
// and re-mints a new one once the current cert passes its half-life - so the
// control plane never presents an expired cert when dialing workers.
func TestRotatingClientCert_ReMintsWhenPastHalfLife(t *testing.T) {
	var calls int
	mint := func() (tls.Certificate, error) {
		calls++
		// Valid for ~2s with a 1s half-life so rotation is observable quickly.
		now := time.Now()
		return selfSignedCert(t, now, now.Add(2*time.Second)), nil
	}

	p, err := newRotatingCert(mint)
	if err != nil {
		t.Fatalf("new rotating cert: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 eager mint, got %d", calls)
	}

	first, err := p.getClientCertificate(nil)
	if err != nil {
		t.Fatalf("get 1: %v", err)
	}
	if calls != 1 {
		t.Fatalf("re-minted while cert still fresh (calls=%d)", calls)
	}
	firstSerial := serialOf(t, first)

	// Cross the half-life; the next get must re-mint and return the new cert.
	time.Sleep(1100 * time.Millisecond)
	second, err := p.getClientCertificate(nil)
	if err != nil {
		t.Fatalf("get 2: %v", err)
	}
	if calls != 2 {
		t.Fatalf("did not re-mint past half-life (calls=%d)", calls)
	}
	if serialOf(t, second) == firstSerial {
		t.Fatal("provider returned the stale cert after re-mint")
	}
}

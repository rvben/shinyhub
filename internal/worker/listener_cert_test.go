package worker

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"
	"time"
)

// TestRotatingCert_GetCertificate_ReMintsPastHalfLife verifies the listener's
// server-cert provider mints eagerly, reuses the cert while fresh, and re-mints
// once the current cert passes its half-life - so the worker-API listener never
// serves an expired cert on a long-running control plane.
func TestRotatingCert_GetCertificate_ReMintsPastHalfLife(t *testing.T) {
	var calls int
	mint := func() (tls.Certificate, error) {
		calls++
		now := time.Now()
		// ~2s life, ~1s half-life so rotation is observable quickly.
		return selfSignedCert(t, now, now.Add(2*time.Second)), nil
	}

	p, err := newRotatingCert(mint)
	if err != nil {
		t.Fatalf("new rotating cert: %v", err)
	}
	first, err := p.getCertificate(nil)
	if err != nil {
		t.Fatalf("get 1: %v", err)
	}
	if calls != 1 {
		t.Fatalf("re-minted while cert still fresh (calls=%d)", calls)
	}
	firstSerial := serialOf(t, first)

	time.Sleep(1100 * time.Millisecond)
	second, err := p.getCertificate(nil)
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

// TestRotatingCert_KeepsValidCertWhenReMintFails verifies a transient signing
// failure past half-life does not break the provider while the held cert is
// still valid: it keeps serving the held cert rather than erroring. The held
// cert is pre-dated so it is already past half-life at construction, keeping
// the test deterministic without real-time sleeps.
func TestRotatingCert_KeepsValidCertWhenReMintFails(t *testing.T) {
	now := time.Now()
	var fail bool
	mint := func() (tls.Certificate, error) {
		if fail {
			return tls.Certificate{}, errors.New("ca offline")
		}
		// Past half-life (issued 10s ago) but still valid for another 2s.
		return selfSignedCert(t, now.Add(-10*time.Second), now.Add(2*time.Second)), nil
	}

	p, err := newRotatingCert(mint)
	if err != nil {
		t.Fatalf("new rotating cert: %v", err)
	}
	fail = true
	if _, err := p.getCertificate(nil); err != nil {
		t.Fatalf("expected fallback to the still-valid cert, got err: %v", err)
	}
}

// TestRotatingCert_ErrorsWhenHeldCertExpiredAndReMintFails verifies the provider
// surfaces the re-mint error once the held cert has actually expired, rather
// than handing out an expired cert. The held cert is pre-dated as expired.
func TestRotatingCert_ErrorsWhenHeldCertExpiredAndReMintFails(t *testing.T) {
	now := time.Now()
	var fail bool
	mint := func() (tls.Certificate, error) {
		if fail {
			return tls.Certificate{}, errors.New("ca offline")
		}
		// Already expired one second ago.
		return selfSignedCert(t, now.Add(-10*time.Second), now.Add(-time.Second)), nil
	}

	p, err := newRotatingCert(mint)
	if err != nil {
		t.Fatalf("new rotating cert: %v", err)
	}
	fail = true
	if _, err := p.getCertificate(nil); err == nil {
		t.Fatal("expected an error once the held cert expired and re-mint fails")
	}
}

// TestCA_ListenerTLSConfig verifies the worker-API listener config serves a
// CA-signed server cert for the requested host through GetCertificate (so it
// can rotate without a restart) and verifies client certs against the CA
// without requiring them, matching register-before-cert.
func TestCA_ListenerTLSConfig(t *testing.T) {
	ca, err := OpenCA(t.TempDir(), []string{"tok"})
	if err != nil {
		t.Fatalf("open ca: %v", err)
	}
	conf, err := ca.ListenerTLSConfig("worker.example.internal")
	if err != nil {
		t.Fatalf("listener tls config: %v", err)
	}
	if conf.ClientAuth != tls.VerifyClientCertIfGiven {
		t.Errorf("ClientAuth = %v, want VerifyClientCertIfGiven", conf.ClientAuth)
	}
	if conf.ClientCAs == nil {
		t.Error("ClientCAs not set")
	}
	if conf.GetCertificate == nil {
		t.Fatal("GetCertificate not wired (listener cert cannot rotate)")
	}

	cert, err := conf.GetCertificate(&tls.ClientHelloInfo{ServerName: "worker.example.internal"})
	if err != nil {
		t.Fatalf("get certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     ca.Pool(),
		DNSName:   "worker.example.internal",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Errorf("listener server cert does not chain to the CA: %v", err)
	}
}

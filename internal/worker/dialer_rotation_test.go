package worker

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"sync"
	"sync/atomic"
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

// TestRotatingCert_ConcurrentReadDuringRotation runs many concurrent handshakes
// (current() callers) against a provider whose cert is always past half-life, so
// every call re-mints. The certificate the TLS stack reads must be stable for
// the duration of the handshake: returning a pointer to the provider's own field
// lets a concurrent re-mint overwrite the struct mid-read. Run under -race, this
// fails when current() shares the live field and passes when it returns a copy.
func TestRotatingCert_ConcurrentReadDuringRotation(t *testing.T) {
	// Pre-build certs on the test goroutine (selfSignedCert calls t.Fatalf, which
	// is illegal off the test goroutine). Each is already past half-life, so every
	// current() call triggers a refresh that overwrites the held cert.
	pool := make([]tls.Certificate, 8)
	for i := range pool {
		now := time.Now()
		pool[i] = selfSignedCert(t, now.Add(-2*time.Hour), now.Add(time.Hour))
	}
	var idx int64
	mint := func() (tls.Certificate, error) {
		n := atomic.AddInt64(&idx, 1)
		return pool[n%int64(len(pool))], nil
	}

	p, err := newRotatingCert(mint)
	if err != nil {
		t.Fatalf("new rotating cert: %v", err)
	}

	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 400; j++ {
				c, err := p.current()
				if err != nil {
					t.Errorf("current: %v", err)
					return
				}
				// Read the fields the TLS stack reads during a handshake; these
				// must not be mutated by a concurrent re-mint.
				if len(c.Certificate) == 0 || c.PrivateKey == nil {
					t.Errorf("torn certificate: %d chains, key=%v", len(c.Certificate), c.PrivateKey)
				}
			}
		}()
	}
	wg.Wait()
}

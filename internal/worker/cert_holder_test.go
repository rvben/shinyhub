package worker

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCertHolder_HotReloadsServerCert verifies that a TLS server reading its
// certificate from a CertHolder via GetCertificate picks up a swapped cert on
// the next handshake, with no listener restart. This is the mechanism that lets
// a worker rotate its expiring server cert without dropping its routing surface.
func TestCertHolder_HotReloadsServerCert(t *testing.T) {
	ca, err := OpenCA(t.TempDir(), []string{"tok"})
	if err != nil {
		t.Fatalf("open ca: %v", err)
	}
	// A DNS hostname (not an IP literal) so the client sends SNI and the server's
	// GetCertificate callback is invoked on every handshake.
	cert1, err := ca.ServerCertificate("worker.test")
	if err != nil {
		t.Fatalf("server cert 1: %v", err)
	}
	cert2, err := ca.ServerCertificate("worker.test")
	if err != nil {
		t.Fatalf("server cert 2: %v", err)
	}

	holder := NewCertHolder(cert1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{
		GetCertificate: holder.GetCertificate,
		MinVersion:     tls.VersionTLS12,
	}
	srv.StartTLS()
	defer srv.Close()

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.CertPEM()) {
		t.Fatal("append CA PEM")
	}
	// A fresh client per call so no TLS connection is reused: each GET is a new
	// handshake that re-invokes GetCertificate.
	serial := func() string {
		c := &http.Client{Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{RootCAs: pool, ServerName: "worker.test", MinVersion: tls.VersionTLS12},
			DisableKeepAlives: true,
		}}
		resp, err := c.Get(srv.URL)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		if len(resp.TLS.PeerCertificates) == 0 {
			t.Fatal("no peer certificate presented")
		}
		return resp.TLS.PeerCertificates[0].SerialNumber.String()
	}

	before := serial()
	holder.Set(cert2)
	after := serial()

	if before == after {
		t.Fatalf("server presented the same cert after Set; hot-reload did not take effect (serial %s)", before)
	}
}

// TestCertHolder_GetClientCertificate verifies the holder also serves the
// client-side callback, returning the current certificate.
func TestCertHolder_GetClientCertificate(t *testing.T) {
	ca, err := OpenCA(t.TempDir(), []string{"tok"})
	if err != nil {
		t.Fatalf("open ca: %v", err)
	}
	cert := newWorkerClientCert(t, ca, "node-holder")
	holder := NewCertHolder(cert)

	got, err := holder.GetClientCertificate(&tls.CertificateRequestInfo{})
	if err != nil {
		t.Fatalf("get client cert: %v", err)
	}
	if len(got.Certificate) == 0 || got.Certificate[0] == nil {
		t.Fatal("holder returned an empty client certificate")
	}
}

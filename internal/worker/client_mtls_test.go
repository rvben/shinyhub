// internal/worker/client_mtls_test.go
package worker

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	workerapi "github.com/rvben/shinyhub/internal/worker/api"
)

// newWorkerClientCert generates a fresh ECDSA key, submits a CSR signed by ca,
// and returns the tls.Certificate the worker presents during the TLS handshake.
func newWorkerClientCert(t *testing.T, ca *CA, nodeID string) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate worker key: %v", err)
	}

	csrTmpl := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "shinyhub-worker"},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTmpl, key)
	if err != nil {
		t.Fatalf("create csr: %v", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	certPEM, err := ca.SignWorkerCSR(nodeID, csrPEM, time.Hour)
	if err != nil {
		t.Fatalf("sign csr: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("x509 key pair: %v", err)
	}
	return tlsCert
}

// newMTLSServer builds an httptest server that requires and verifies client
// certificates signed by ca. The caller is responsible for srv.Close().
func newMTLSServer(t *testing.T, ca *CA, mux *http.ServeMux) *httptest.Server {
	t.Helper()

	serverCert, err := ca.ServerCertificate()
	if err != nil {
		t.Fatalf("server cert: %v", err)
	}

	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    ca.Pool(),
		MinVersion:   tls.VersionTLS12,
	}
	srv.StartTLS()
	return srv
}

// TestClientMTLSRoundTrip verifies that NewClient presents its client cert and
// pins the CA when talking to an mTLS-only server.
func TestClientMTLSRoundTrip(t *testing.T) {
	ca, err := OpenCA(t.TempDir(), []string{"tok"})
	if err != nil {
		t.Fatalf("open ca: %v", err)
	}

	clientCert := newWorkerClientCert(t, ca, "node-mtls")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/workers/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(workerapi.HeartbeatResponse{})
	})
	mux.HandleFunc("/internal/bundles/sha256:abc", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/bundles/sha256:abc" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte("zip-bytes"))
	})

	srv := newMTLSServer(t, ca, mux)
	defer srv.Close()

	caHolder, err := NewCAHolder(ca.CertPEM())
	if err != nil {
		t.Fatalf("ca holder: %v", err)
	}
	c, err := NewClient(srv.URL, NewCertHolder(clientCert), caHolder)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	// Heartbeat must succeed over the mTLS connection.
	if _, err := c.Heartbeat(t.Context(), "v-test", ""); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	// FetchBundle must succeed and return the expected body.
	rc, err := c.FetchBundle(t.Context(), "sha256:abc")
	if err != nil {
		t.Fatalf("fetch bundle: %v", err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "zip-bytes" {
		t.Fatalf("body = %q, want %q", body, "zip-bytes")
	}
}

// TestClientMTLSRejectsNoClientCert verifies that a client without a cert
// cannot complete the TLS handshake when the server requires client auth.
func TestClientMTLSRejectsNoClientCert(t *testing.T) {
	ca, err := OpenCA(t.TempDir(), []string{"tok"})
	if err != nil {
		t.Fatalf("open ca: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/workers/heartbeat", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := newMTLSServer(t, ca, mux)
	defer srv.Close()

	// Build a pool trusting the CA but provide NO client certificate.
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.CertPEM()) {
		t.Fatal("append CA PEM")
	}
	httpc := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    pool,
				MinVersion: tls.VersionTLS12,
			},
		},
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/workers/heartbeat", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	_, doErr := httpc.Do(req)
	if doErr == nil {
		t.Fatal("expected TLS handshake failure but request succeeded")
	}
}

package api

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/worker"
	workerapi "github.com/rvben/shinyhub/internal/worker/api"
)

// newCSR returns a fresh ECDSA key (as PEM) and a CSR PEM for it.
func newCSR(t *testing.T) (keyPEM, csrPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "shinyhub-worker"},
	}, key)
	if err != nil {
		t.Fatalf("create csr: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	csrPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	return keyPEM, csrPEM
}

// heartbeatTestServer stands up an mTLS listener serving the heartbeat handler,
// returning the server and a client that presents clientCert.
func heartbeatTestServer(t *testing.T, ca *worker.CA, wapi *WorkerAPI, clientCert tls.Certificate) (*httptest.Server, *http.Client) {
	t.Helper()
	cpCert, err := ca.ServerCertificate("127.0.0.1")
	if err != nil {
		t.Fatalf("cp server cert: %v", err)
	}
	r := chi.NewRouter()
	r.Post("/api/workers/heartbeat", wapi.HandleHeartbeat)
	srv := httptest.NewUnstartedServer(r)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{cpCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    ca.Pool(),
		MinVersion:   tls.VersionTLS12,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.CertPEM()) {
		t.Fatal("append CA pem")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      pool,
		ServerName:   "127.0.0.1",
		MinVersion:   tls.VersionTLS12,
	}}}
	return srv, client
}

func postHeartbeat(t *testing.T, client *http.Client, url string, req workerapi.HeartbeatRequest) workerapi.HeartbeatResponse {
	t.Helper()
	body, _ := json.Marshal(req)
	resp, err := client.Post(url+"/api/workers/heartbeat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("heartbeat post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat status = %d, want 200", resp.StatusCode)
	}
	var out workerapi.HeartbeatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

// TestHandleHeartbeat_RenewsCertWhenCSRProvided verifies that a heartbeat
// carrying a renewal CSR comes back with a freshly signed certificate that is
// CA-verifiable, bound to the same node id, and valid for a full TTL beyond the
// original cert's expiry.
func TestHandleHeartbeat_RenewsCertWhenCSRProvided(t *testing.T) {
	store := newRenewalTestStore(t)
	ca, err := worker.OpenCA(t.TempDir(), []string{"tok"})
	if err != nil {
		t.Fatalf("open ca: %v", err)
	}
	reg, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	node, err := reg.Register(worker.RegisterParams{Tier: "remote", AdvertiseAddr: "127.0.0.1:9"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	keyPEM, csrPEM := newCSR(t)
	// Original cert with a deliberately short life so renewal clearly extends it.
	origPEM, err := ca.SignWorkerCSR(node.NodeID, csrPEM, 2*time.Second)
	if err != nil {
		t.Fatalf("sign original: %v", err)
	}
	clientCert, err := tls.X509KeyPair(origPEM, keyPEM)
	if err != nil {
		t.Fatalf("client keypair: %v", err)
	}
	origLeaf, _ := x509.ParseCertificate(clientCert.Certificate[0])

	wapi := NewWorkerAPI(store, reg, ca, "")
	wapi.certTTL = time.Hour
	srv, client := heartbeatTestServer(t, ca, wapi, clientCert)

	resp := postHeartbeat(t, client, srv.URL, workerapi.HeartbeatRequest{Version: "v1", RenewCSRPEM: string(csrPEM)})
	if resp.CertPEM == "" {
		t.Fatal("heartbeat with renewal CSR returned no certificate")
	}

	block, _ := pem.Decode([]byte(resp.CertPEM))
	if block == nil {
		t.Fatal("renewed cert is not valid PEM")
	}
	renewed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse renewed cert: %v", err)
	}
	// Same node identity.
	if got := worker.NodeIDFromCert(renewed); got != node.NodeID {
		t.Errorf("renewed cert node id = %q, want %q", got, node.NodeID)
	}
	// Verifiable against the CA.
	if _, err := renewed.Verify(x509.VerifyOptions{
		Roots:     ca.Pool(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Errorf("renewed cert does not verify against CA: %v", err)
	}
	// Validity extended well past the original short-lived cert.
	if !renewed.NotAfter.After(origLeaf.NotAfter.Add(30 * time.Minute)) {
		t.Errorf("renewed NotAfter %s did not extend past original %s", renewed.NotAfter, origLeaf.NotAfter)
	}
}

// TestHandleHeartbeat_NoRenewalWhenNoCSR verifies a plain heartbeat returns no
// certificate, so renewal only happens when explicitly requested.
func TestHandleHeartbeat_NoRenewalWhenNoCSR(t *testing.T) {
	store := newRenewalTestStore(t)
	ca, err := worker.OpenCA(t.TempDir(), []string{"tok"})
	if err != nil {
		t.Fatalf("open ca: %v", err)
	}
	reg, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	node, err := reg.Register(worker.RegisterParams{Tier: "remote", AdvertiseAddr: "127.0.0.1:9"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	keyPEM, csrPEM := newCSR(t)
	origPEM, err := ca.SignWorkerCSR(node.NodeID, csrPEM, time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	clientCert, err := tls.X509KeyPair(origPEM, keyPEM)
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}

	wapi := NewWorkerAPI(store, reg, ca, "")
	srv, client := heartbeatTestServer(t, ca, wapi, clientCert)

	resp := postHeartbeat(t, client, srv.URL, workerapi.HeartbeatRequest{Version: "v1"})
	if resp.CertPEM != "" {
		t.Errorf("plain heartbeat returned a certificate: %q", resp.CertPEM)
	}
}

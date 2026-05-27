package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/worker"
)

// certForNode registers a node, signs a worker cert binding its node id, and
// attaches it to r as the presented client cert. It returns the node id so the
// caller can revoke it.
func certForNode(t *testing.T, r *http.Request, h *WorkerAPI, tier string) (*http.Request, string) {
	t.Helper()
	node, err := h.registry.Register(worker.RegisterParams{Tier: tier, AdvertiseAddr: "1.1.1.1:1"})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "w"}}, key)
	if err != nil {
		t.Fatalf("create csr: %v", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	certPEM, err := h.ca.SignWorkerCSR(node.NodeID, csrPEM, time.Hour)
	if err != nil {
		t.Fatalf("sign csr: %v", err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	return r, node.NodeID
}

// TestHeartbeatRejectsRevokedWorker asserts a revoked worker's still-valid cert
// is rejected on the heartbeat endpoint immediately, even though the cert has
// not expired.
func TestHeartbeatRejectsRevokedWorker(t *testing.T) {
	h := newWorkerTestHandler(t, []string{"good-token"})
	req := httptest.NewRequest(http.MethodPost, "/api/workers/heartbeat", http.NoBody)
	req, nodeID := certForNode(t, req, h, "burst")

	// Sanity: the cert authenticates before revocation.
	w := httptest.NewRecorder()
	h.HandleHeartbeat(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("pre-revoke heartbeat status = %d, want 200: %s", w.Code, w.Body.String())
	}

	if err := h.registry.Revoke(nodeID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/workers/heartbeat", http.NoBody)
	req2.TLS = req.TLS // same (still-valid) cert
	w2 := httptest.NewRecorder()
	h.HandleHeartbeat(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("post-revoke heartbeat status = %d, want 401", w2.Code)
	}
}

// TestBundleFetchRejectsRevokedWorker asserts a revoked worker cannot pull
// bundles, even within its cert TTL.
func TestBundleFetchRejectsRevokedWorker(t *testing.T) {
	h := newWorkerTestHandler(t, []string{"good-token"})
	req := httptest.NewRequest(http.MethodGet, "/internal/bundles/sha256:abc", http.NoBody)
	req, nodeID := certForNode(t, req, h, "burst")
	req = withURLParam(req, "digest", "sha256:abc")

	if err := h.registry.Revoke(nodeID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	w := httptest.NewRecorder()
	h.HandleBundleFetch(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("post-revoke bundle fetch status = %d, want 401", w.Code)
	}
}

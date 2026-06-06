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

	"github.com/rvben/shinyhub/internal/worker"
	workerapi "github.com/rvben/shinyhub/internal/worker/api"
)

// signedCertReq builds a request carrying a client cert signed by h.ca for the
// given node id, so authenticatedNodeID derives that node from the TLS state.
func signedCertReq(t *testing.T, h *WorkerAPI, nodeID, method, target string) *http.Request {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "w"}}, key)
	if err != nil {
		t.Fatalf("create csr: %v", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	certPEM, err := h.ca.SignWorkerCSR(nodeID, csrPEM, time.Hour)
	if err != nil {
		t.Fatalf("sign csr: %v", err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	req := httptest.NewRequest(method, target, nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	return req
}

func registerReq(t *testing.T, addr string) *http.Request {
	t.Helper()
	body, _ := json.Marshal(workerapi.RegisterRequest{
		Token: "good-token", Tier: "burst", AdvertiseAddr: addr, CSRPEM: string(mustCSR(t)),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/workers/register", bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.9:1"
	return req
}

func TestHandleRegister_503WhenNotOwner(t *testing.T) {
	h := newWorkerTestHandler(t, []string{"good-token"})
	h.SetOwnership(func() bool { return false })

	w := httptest.NewRecorder()
	h.HandleRegister(w, registerReq(t, "203.0.113.5:9000"))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	// The rejected register must not have created any worker row.
	ws, err := h.store.ListWorkers()
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 0 {
		t.Fatalf("non-owner created %d worker rows, want 0", len(ws))
	}
}

func TestHandleHeartbeat_503WhenNotOwner(t *testing.T) {
	h := newWorkerTestHandler(t, []string{"good-token"})
	// Register a worker (nil predicate => serve), then flip to non-owner.
	node, err := h.registry.Register(worker.RegisterParams{Name: "hb", Tier: "burst", AdvertiseAddr: "203.0.113.5:9000"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	h.SetOwnership(func() bool { return false })

	req := signedCertReq(t, h, node.NodeID, http.MethodPost, "/api/workers/heartbeat")
	w := httptest.NewRecorder()
	h.HandleHeartbeat(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	// The worker must not have been promoted (a served heartbeat would set it up).
	got, _ := h.store.GetWorker(node.NodeID)
	if got == nil || got.Status == "up" {
		t.Fatalf("heartbeat on a non-owner mutated worker status to %v", got)
	}
}

func TestHandleRegister_ServesWhenOwnerTrue(t *testing.T) {
	h := newWorkerTestHandler(t, []string{"good-token"})
	h.SetOwnership(func() bool { return true })

	w := httptest.NewRecorder()
	h.HandleRegister(w, registerReq(t, "203.0.113.5:9000"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
}

func TestWorkerMutations_503WhenOwnerButNotReady(t *testing.T) {
	h := newWorkerTestHandler(t, []string{"good-token"})
	owner, ready := true, false
	h.SetOwnership(func() bool { return owner && ready })

	send := func() int {
		w := httptest.NewRecorder()
		h.HandleRegister(w, registerReq(t, "203.0.113.5:9000"))
		return w.Code
	}
	if code := send(); code != http.StatusServiceUnavailable {
		t.Fatalf("owner-but-not-ready: status = %d, want 503", code)
	}
	ready = true
	if code := send(); code != http.StatusOK {
		t.Fatalf("owner-and-ready: status = %d, want 200", code)
	}
}

func TestHandleBundleFetch_DBAuthoritative_StaleNegative(t *testing.T) {
	// A worker present in the shared store but absent from THIS instance's index
	// (registered after this instance built its registry) must still authenticate
	// via the authoritative store read - the open bundle-fetch is instance-
	// independent. We register it through a second registry over the same store.
	h := newWorkerTestHandler(t, []string{"good-token"})
	other, err := worker.NewRegistry(h.store)
	if err != nil {
		t.Fatalf("second registry: %v", err)
	}
	node, err := other.Register(worker.RegisterParams{Name: "stale-neg", Tier: "burst", AdvertiseAddr: "203.0.113.6:9000"})
	if err != nil {
		t.Fatalf("register via other: %v", err)
	}
	// Precondition: this instance's index does NOT know the worker.
	if _, ok := h.registry.Worker(node.NodeID); ok {
		t.Fatal("precondition failed: worker already in this instance's index")
	}

	const digest = "sha256:none"
	req := signedCertReq(t, h, node.NodeID, http.MethodGet, "/internal/bundles/"+digest)
	req = withURLParam(req, "digest", digest)
	w := httptest.NewRecorder()
	h.HandleBundleFetch(w, req)
	// Auth passed via the store; the (unseeded) digest then yields 404, NOT 401.
	if w.Code == http.StatusUnauthorized {
		t.Fatal("bundle-fetch rejected a worker present in the store but absent from byID")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (auth passed, digest absent)", w.Code)
	}
}

func TestHandleBundleFetch_DBAuthoritative_StalePositiveRevoked(t *testing.T) {
	// A worker REVOKED in the store but still present-and-unrevoked in this
	// instance's stale index must be rejected: auth reads the authoritative store,
	// not byID. A byID fast path would wrongly authenticate the revoked worker.
	h := newWorkerTestHandler(t, []string{"good-token"})
	node, err := h.registry.Register(worker.RegisterParams{Name: "stale-pos", Tier: "burst", AdvertiseAddr: "203.0.113.7:9000"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	// Revoke only in the store, leaving this instance's index stale-positive.
	if err := h.store.RevokeWorker(node.NodeID); err != nil {
		t.Fatalf("revoke in store: %v", err)
	}
	// Precondition: the in-memory index still holds an unrevoked copy.
	if w, ok := h.registry.Worker(node.NodeID); !ok || w.Revoked() {
		t.Fatalf("precondition failed: index not stale-positive (ok=%v revoked=%v)", ok, ok && w.Revoked())
	}

	const digest = "sha256:none"
	req := signedCertReq(t, h, node.NodeID, http.MethodGet, "/internal/bundles/"+digest)
	req = withURLParam(req, "digest", digest)
	w := httptest.NewRecorder()
	h.HandleBundleFetch(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (revoked worker must not authenticate off a stale index)", w.Code)
	}
}

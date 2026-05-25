package api

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/worker"
	workerapi "github.com/rvben/shinyhub/internal/worker/api"
)

func mustCSR(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "w"},
	}, key)
	if err != nil {
		t.Fatalf("create csr: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func newTestStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestRegisterRejectsBadToken(t *testing.T) {
	h := newWorkerTestHandler(t, []string{"good-token"})
	body, _ := json.Marshal(workerapi.RegisterRequest{
		Token: "bad-token", Tier: "burst", AdvertiseAddr: "1.2.3.4:9", CSRPEM: string(mustCSR(t)),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/workers/register", bytes.NewReader(body))
	req.RemoteAddr = "9.9.9.9:1"
	w := httptest.NewRecorder()
	h.handleWorkerRegister(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestRegisterSignsAndPersists(t *testing.T) {
	h := newWorkerTestHandler(t, []string{"good-token"})
	body, _ := json.Marshal(workerapi.RegisterRequest{
		Token: "good-token", Name: "burst-a", Tier: "burst",
		AdvertiseAddr: "10.0.0.5:8443", Version: "v0.6.0", CSRPEM: string(mustCSR(t)),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/workers/register", bytes.NewReader(body))
	req.RemoteAddr = "9.9.9.9:1"
	w := httptest.NewRecorder()
	h.handleWorkerRegister(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp workerapi.RegisterResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.NodeID == "" || resp.CertPEM == "" || resp.CABundle == "" {
		t.Fatalf("incomplete response: %+v", resp)
	}
	// The issued cert binds the allocated node id.
	block, _ := pem.Decode([]byte(resp.CertPEM))
	cert, _ := x509.ParseCertificate(block.Bytes)
	if worker.NodeIDFromCert(cert) != resp.NodeID {
		t.Fatalf("issued cert node id %q != allocated %q", worker.NodeIDFromCert(cert), resp.NodeID)
	}
	if _, ok := h.registry.WorkerForTier("burst"); !ok {
		t.Fatal("worker not indexed after register")
	}
}

func TestRegisterRateLimited(t *testing.T) {
	h := newWorkerTestHandler(t, []string{"good-token"})
	send := func() int {
		body, _ := json.Marshal(workerapi.RegisterRequest{
			Token: "bad", Tier: "burst", AdvertiseAddr: "1.2.3.4:9", CSRPEM: string(mustCSR(t)),
		})
		req := httptest.NewRequest(http.MethodPost, "/api/workers/register", bytes.NewReader(body))
		req.RemoteAddr = "5.5.5.5:1"
		w := httptest.NewRecorder()
		h.handleWorkerRegister(w, req)
		return w.Code
	}
	// Burst of registrations from one source eventually trips the limiter (429).
	// The limiter is configured with burst=5, so any call after the 5th from a
	// single source address must return 429.
	got429 := false
	for i := 0; i < 50; i++ {
		if send() == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("rate limiter never tripped under burst from a single source")
	}
}

func newWorkerTestHandler(t *testing.T, tokens []string) *WorkerAPI {
	t.Helper()
	store := newTestStore(t)
	ca, err := worker.OpenCA(t.TempDir(), tokens)
	if err != nil {
		t.Fatalf("ca: %v", err)
	}
	reg, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return NewWorkerAPI(store, reg, ca)
}

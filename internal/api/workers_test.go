package api

import (
	"bytes"
	"context"
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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
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
	return NewWorkerAPI(store, reg, ca, "")
}

func withURLParam(r *http.Request, key, val string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func withWorkerCert(t *testing.T, r *http.Request, h *WorkerAPI, name string) *http.Request {
	t.Helper()
	node, err := h.registry.Register(worker.RegisterParams{Name: name, Tier: "burst", AdvertiseAddr: "1.1.1.1:1"})
	if err != nil {
		t.Fatalf("withWorkerCert: register node: %v", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("withWorkerCert: generate key: %v", err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "w"}}, key)
	if err != nil {
		t.Fatalf("withWorkerCert: create csr: %v", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	certPEM, err := h.ca.SignWorkerCSR(node.NodeID, csrPEM, time.Hour)
	if err != nil {
		t.Fatalf("withWorkerCert: sign csr: %v", err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("withWorkerCert: parse cert: %v", err)
	}
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	return r
}

func TestBundleFetchStreamsZipByDigest(t *testing.T) {
	h := newWorkerTestHandler(t, []string{"good-token"})

	// CreateApp requires a real owner; create one first.
	if err := h.store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "developer"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	owner, err := h.store.GetUserByUsername("owner")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}

	// Seed an app + deployment whose digest points at a real zip on disk.
	// CreateApp returns only error, so fetch the app by slug afterwards.
	if err := h.store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID, Access: "private"}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app, err := h.store.GetAppBySlug("demo")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	dep, _ := h.store.BeginDeployment(app.ID, "v1", "/bundles/demo/v1")
	const digest = "sha256:deadbeef"
	_ = h.store.SetDeploymentDigest(dep.ID, digest)
	_ = h.store.PromoteDeployment(dep.ID)

	// Place a zip where the handler expects it: <appsDir>/<slug>/bundles/<version>.zip
	h.appsDir = t.TempDir()
	zipDir := filepath.Join(h.appsDir, "demo", "bundles")
	if err := os.MkdirAll(zipDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	want := []byte("PK\x03\x04 fake-zip-bytes")
	if err := os.WriteFile(filepath.Join(zipDir, "v1.zip"), want, 0o644); err != nil {
		t.Fatalf("write zip: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/internal/bundles/"+digest, nil)
	req = withWorkerCert(t, req, h, "node-auth")
	req = withURLParam(req, "digest", digest)
	w := httptest.NewRecorder()
	h.handleBundleFetch(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !bytes.Equal(w.Body.Bytes(), want) {
		t.Fatalf("body mismatch: got %q", w.Body.Bytes())
	}
}

func TestBundleFetchRejectsUnauthenticated(t *testing.T) {
	h := newWorkerTestHandler(t, []string{"good-token"})

	// No TLS state on the request - authenticatedNodeID must reject it.
	req := httptest.NewRequest(http.MethodGet, "/internal/bundles/sha256:aabbcc", nil)
	req = withURLParam(req, "digest", "sha256:aabbcc")
	w := httptest.NewRecorder()
	h.handleBundleFetch(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if body["error"] == "" {
		t.Fatalf("expected non-empty error field, got %v", body)
	}
}

func TestBundleFetchUnknownDigest(t *testing.T) {
	h := newWorkerTestHandler(t, []string{"good-token"})

	// Authenticated node, but the digest has no matching deployment row.
	req := httptest.NewRequest(http.MethodGet, "/internal/bundles/sha256:notfound", nil)
	req = withWorkerCert(t, req, h, "node-unknown-digest")
	req = withURLParam(req, "digest", "sha256:notfound")
	w := httptest.NewRecorder()
	h.handleBundleFetch(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if body["error"] == "" {
		t.Fatalf("expected non-empty error field, got %v", body)
	}
}

func TestBundleFetchMissingArtifact(t *testing.T) {
	h := newWorkerTestHandler(t, []string{"good-token"})

	// Seed a real user, app, and deployment so the DB lookup succeeds.
	if err := h.store.CreateUser(db.CreateUserParams{Username: "owner2", PasswordHash: "h", Role: "developer"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	owner, err := h.store.GetUserByUsername("owner2")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if err := h.store.CreateApp(db.CreateAppParams{Slug: "nozip", Name: "NoZip", OwnerID: owner.ID, Access: "private"}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app, err := h.store.GetAppBySlug("nozip")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	dep, err := h.store.BeginDeployment(app.ID, "v1", "/bundles/nozip/v1")
	if err != nil {
		t.Fatalf("begin deployment: %v", err)
	}
	const digest = "sha256:missingzip"
	if err := h.store.SetDeploymentDigest(dep.ID, digest); err != nil {
		t.Fatalf("set digest: %v", err)
	}
	if err := h.store.PromoteDeployment(dep.ID); err != nil {
		t.Fatalf("promote: %v", err)
	}

	// appsDir is an empty temp dir - no zip file exists at the resolved path.
	h.appsDir = t.TempDir()

	req := httptest.NewRequest(http.MethodGet, "/internal/bundles/"+digest, nil)
	req = withWorkerCert(t, req, h, "node-missing-artifact")
	req = withURLParam(req, "digest", digest)
	w := httptest.NewRecorder()
	h.handleBundleFetch(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if body["error"] == "" {
		t.Fatalf("expected non-empty error field, got %v", body)
	}
}

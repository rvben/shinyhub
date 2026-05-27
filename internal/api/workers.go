package api

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/worker"
	workerapi "github.com/rvben/shinyhub/internal/worker/api"
	"golang.org/x/time/rate"
)

// WorkerAPI serves the worker-facing endpoints (register, heartbeat, bundle
// fetch). It is mounted on a dedicated mTLS listener; the register path is the
// only one reachable before a client cert exists and is rate-limited per source.
type WorkerAPI struct {
	store    *db.Store
	registry *worker.Registry
	ca       *worker.CA
	appsDir  string
	certTTL  time.Duration

	limMu sync.Mutex
	// limiters holds one rate.Limiter per source host. Entries are created on
	// demand and never evicted, which is acceptable because the worker
	// source-IP set is bounded to known hosts.
	limiters map[string]*rate.Limiter
}

// NewWorkerAPI constructs the worker API with a default short cert TTL.
// appsDir is the root directory under which per-app bundle zips are stored;
// it may be empty during tests that override appsDir directly.
func NewWorkerAPI(store *db.Store, reg *worker.Registry, ca *worker.CA, appsDir string) *WorkerAPI {
	return &WorkerAPI{
		store:    store,
		registry: reg,
		ca:       ca,
		appsDir:  appsDir,
		certTTL:  1 * time.Hour,
		limiters: map[string]*rate.Limiter{},
	}
}

// registerLimiter returns the per-source limiter, creating it on first use.
// Allows a small burst then ~1 register/sec, enough for legitimate retries while
// throttling token-guessing.
func (a *WorkerAPI) registerLimiter(host string) *rate.Limiter {
	a.limMu.Lock()
	defer a.limMu.Unlock()
	l, ok := a.limiters[host]
	if !ok {
		l = rate.NewLimiter(rate.Every(time.Second), 5)
		a.limiters[host] = l
	}
	return l
}

func sourceHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *WorkerAPI) HandleRegister(w http.ResponseWriter, r *http.Request) {
	if !a.registerLimiter(sourceHost(r)).Allow() {
		writeError(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	var req workerapi.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if !a.ca.VerifyJoinToken(req.Token) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if req.Tier == "" || req.AdvertiseAddr == "" || req.CSRPEM == "" {
		writeError(w, http.StatusBadRequest, "missing tier, advertise_addr, or csr")
		return
	}
	node, err := a.registry.Register(worker.RegisterParams{
		Name:          req.Name,
		AdvertiseAddr: req.AdvertiseAddr,
		Tier:          req.Tier,
		Version:       req.Version,
	})
	if err != nil {
		slog.Error("worker register: persist failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	certPEM, err := a.ca.SignWorkerCSR(node.NodeID, []byte(req.CSRPEM), a.certTTL)
	if err != nil {
		slog.Error("worker register: sign failed", "node", node.NodeID, "err", err)
		writeError(w, http.StatusBadRequest, "bad csr")
		return
	}
	writeJSON(w, http.StatusOK, workerapi.RegisterResponse{
		NodeID:   node.NodeID,
		CertPEM:  string(certPEM),
		CABundle: string(a.ca.CertPEM()),
	})
}

func (a *WorkerAPI) HandleHeartbeat(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := a.authenticatedNodeID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req workerapi.HeartbeatRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	fingerprint := worker.Fingerprint(r.TLS.PeerCertificates[0])
	if err := a.registry.Heartbeat(nodeID, fingerprint); err != nil {
		writeError(w, http.StatusUnauthorized, "unknown node")
		return
	}
	// Renew the worker's certificate when it submits a CSR, binding the same
	// node id so the renewed cert keeps the worker's identity and SAN. The
	// worker presents the current (still-valid) cert on this very call, then
	// swaps in the returned one before the old cert expires.
	var resp workerapi.HeartbeatResponse
	if req.RenewCSRPEM != "" {
		certPEM, err := a.ca.SignWorkerCSR(nodeID, []byte(req.RenewCSRPEM), a.certTTL)
		if err != nil {
			slog.Error("worker heartbeat: renew sign failed", "node", nodeID, "err", err)
			writeError(w, http.StatusBadRequest, "bad csr")
			return
		}
		resp.CertPEM = string(certPEM)
	}
	// Carry the current CA bundle on every heartbeat so a rotated trust root
	// reaches established workers without re-registration. The worker applies it
	// only when it differs from the bundle it already trusts.
	resp.CABundle = string(a.ca.CertPEM())
	writeJSON(w, http.StatusOK, resp)
}

// authenticatedNodeID derives the caller's node id from its client certificate
// and confirms the node is still in the registry (revocation = registry removal).
func (a *WorkerAPI) authenticatedNodeID(r *http.Request) (string, bool) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return "", false
	}
	nodeID := worker.NodeIDFromCert(r.TLS.PeerCertificates[0])
	if nodeID == "" {
		return "", false
	}
	if _, ok := a.registry.Worker(nodeID); !ok {
		return "", false
	}
	return nodeID, true
}

// HandleBundleFetch streams the stored bundle zip for a content digest. The
// caller (agent) verifies the digest on receipt, so this path only resolves the
// digest to a deployment and serves its archived zip artifact.
func (a *WorkerAPI) HandleBundleFetch(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authenticatedNodeID(r); !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	digest := chi.URLParam(r, "digest")
	dep, err := a.store.GetDeploymentByDigest(digest)
	if err != nil {
		writeError(w, http.StatusNotFound, "bundle not found")
		return
	}
	app, err := a.store.GetAppByID(dep.AppID)
	if err != nil {
		writeError(w, http.StatusNotFound, "bundle not found")
		return
	}
	zipPath := filepath.Join(a.appsDir, app.Slug, "bundles", dep.Version+".zip")
	f, err := os.Open(zipPath)
	if err != nil {
		slog.Error("bundle fetch: open artifact", "digest", digest, "path", zipPath, "err", err)
		writeError(w, http.StatusNotFound, "bundle artifact missing")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/zip")
	http.ServeContent(w, r, dep.Version+".zip", time.Time{}, f)
}

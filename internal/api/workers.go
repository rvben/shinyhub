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

	// registerRL throttles the register endpoint per source host. Its
	// sliding-window limiter self-evicts idle source keys on a periodic sweep,
	// so the tracked-source map cannot grow without bound over long uptime.
	registerRL *keyedRateLimiter

	// ownMu guards isOwner, the control-plane ownership-and-readiness predicate
	// that gates the worker mutation endpoints (register/heartbeat). A nil
	// predicate serves unconditionally (tests / single-node-without-elector); the
	// production boot path sets a reject-all predicate before the listener serves
	// and upgrades it to elector-and-ready once that exists.
	ownMu   sync.RWMutex
	isOwner func() bool
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
		// Five registrations per second per source: a small burst for legitimate
		// retries while throttling join-token guessing.
		registerRL: newKeyedRateLimiter(5, time.Second),
	}
}

// SetOwnership wires the control-plane ownership-and-readiness predicate. Until
// it is set (tests, or a single-node build that never constructs an elector) the
// worker mutation endpoints serve unconditionally; the production boot path sets
// a reject-all predicate before the listener serves and upgrades it afterward.
func (a *WorkerAPI) SetOwnership(fn func() bool) {
	a.ownMu.Lock()
	a.isOwner = fn
	a.ownMu.Unlock()
}

// owner reports whether this instance may handle worker mutations. A nil
// predicate means "serve" (see SetOwnership).
func (a *WorkerAPI) owner() bool {
	a.ownMu.RLock()
	fn := a.isOwner
	a.ownMu.RUnlock()
	return fn == nil || fn()
}

func sourceHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *WorkerAPI) HandleRegister(w http.ResponseWriter, r *http.Request) {
	// Worker registration mutates control-plane state (worker rows), so only the
	// ready owner accepts it. A standby returns 503; the agent retries.
	if !a.owner() {
		writeError(w, http.StatusServiceUnavailable, "not control-plane owner")
		return
	}
	if !a.registerRL.allow(sourceHost(r)) {
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
	// Heartbeat mutates worker liveness/cert state, so only the ready owner
	// accepts it (checked before auth). A standby returns 503; the agent retries.
	if !a.owner() {
		writeError(w, http.StatusServiceUnavailable, "not control-plane owner")
		return
	}
	nodeID, ok := a.authenticatedNodeID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req workerapi.HeartbeatRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	fingerprint := worker.Fingerprint(r.TLS.PeerCertificates[0])
	fenced, current, err := a.registry.Heartbeat(nodeID, fingerprint, req.Incarnation)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unknown node")
		return
	}
	var resp workerapi.HeartbeatResponse
	resp.Incarnation = current
	resp.Fenced = fenced
	// Renew the worker's certificate when it submits a CSR, binding the same
	// node id so the renewed cert keeps the worker's identity and SAN. The
	// worker presents the current (still-valid) cert on this very call, then
	// swaps in the returned one before the old cert expires.
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
// and confirms the node exists and is not revoked, reading the AUTHORITATIVE
// store rather than the in-memory routing index. On a standby the index can be
// stale in either direction - missing a worker registered after this instance
// booted, or still holding a worker the active has since revoked - so the open
// bundle-fetch endpoint must consult the shared store, not byID, or a revoked
// worker presenting a still-valid cert could pull bundles off a stale standby.
// The store read is on a non-proxy-hot path (replica start / heartbeat interval).
func (a *WorkerAPI) authenticatedNodeID(r *http.Request) (string, bool) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return "", false
	}
	nodeID := worker.NodeIDFromCert(r.TLS.PeerCertificates[0])
	if nodeID == "" {
		return "", false
	}
	w, err := a.store.GetWorker(nodeID) // ErrNotFound => unauthenticated
	if err != nil || w.Revoked() {
		return "", false
	}
	return nodeID, true
}

// serveBundleByDigest resolves digest to a deployment, opens the stored bundle
// zip, and streams it to w. It is the shared implementation called by both the
// worker mTLS handler (HandleBundleFetch) and the Fargate capability-token
// handler (FargateBundleHandler.Handle). Both callers perform their own
// authentication before delegating here.
func serveBundleByDigest(w http.ResponseWriter, r *http.Request, store *db.Store, appsDir, digest string) {
	dep, err := store.GetDeploymentByDigest(digest)
	if err != nil {
		writeError(w, http.StatusNotFound, "bundle not found")
		return
	}
	app, err := store.GetAppByID(dep.AppID)
	if err != nil {
		writeError(w, http.StatusNotFound, "bundle not found")
		return
	}
	zipPath := filepath.Join(appsDir, app.Slug, "bundles", dep.Version+".zip")
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

// HandleBundleFetch streams the stored bundle zip for a content digest. The
// caller (worker agent) presents a mTLS client certificate for authentication.
// Bundle serving is delegated to the shared serveBundleByDigest helper so both
// the mTLS worker path and the Fargate capability-token path have identical
// behavior.
func (a *WorkerAPI) HandleBundleFetch(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authenticatedNodeID(r); !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	digest := chi.URLParam(r, "digest")
	serveBundleByDigest(w, r, a.store, a.appsDir, digest)
}

package worker

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/storage"
	"github.com/rvben/shinyhub/internal/worker/api"
)

// replicaRecord tracks one running replica on this worker.
type replicaRecord struct {
	token       string
	containerID string
	handle      process.RunHandle
	hostPort    int // host publish port the in-container bind port maps to
}

// ReplicaServerConfig configures a replicaServer. AllocatePort is injectable
// for tests; production wraps deploy.AllocatePort (which returns int, no error).
type ReplicaServerConfig struct {
	Runtime      process.Runtime
	DataDir      string
	NodeID       string
	Advertise    string // host:port base used to build tunnel URLs
	AllocatePort func() int
}

// replicaServer runs and tracks app replicas on a worker and proxies their
// data-plane traffic over the agent's mTLS listener.
type replicaServer struct {
	runtime   process.Runtime
	dataDir   string
	nodeID    string
	advertise string
	allocate  func() int

	mu          sync.RWMutex
	byContainer map[string]*replicaRecord
	byToken     map[string]*replicaRecord
}

// NewReplicaServer constructs a replicaServer from the given config. If
// AllocatePort is nil, deploy.AllocatePort is used.
func NewReplicaServer(cfg ReplicaServerConfig) *replicaServer {
	alloc := cfg.AllocatePort
	if alloc == nil {
		alloc = deploy.AllocatePort
	}
	return &replicaServer{
		runtime:     cfg.Runtime,
		dataDir:     cfg.DataDir,
		nodeID:      cfg.NodeID,
		advertise:   cfg.Advertise,
		allocate:    alloc,
		byContainer: make(map[string]*replicaRecord),
		byToken:     make(map[string]*replicaRecord),
	}
}

// allocatePort returns a free host port for publishing a replica's bind port.
func (s *replicaServer) allocatePort() int { return s.allocate() }

// provisionAppData ensures the worker-local app-data directory for slug exists
// and returns its absolute path.
func (s *replicaServer) provisionAppData(slug string) (string, error) {
	vol := storage.LocalVolume{Root: filepath.Join(s.dataDir, "app-data")}
	ref, err := vol.Provision(slug)
	if err != nil {
		return "", err
	}
	return ref.Path, nil
}

// frameLogWriter writes each Write call as a FrameLog frame and flushes so the
// control plane receives logs continuously over the long-lived response.
type frameLogWriter struct {
	enc     *json.Encoder
	flusher http.Flusher
}

func (w *frameLogWriter) Write(p []byte) (int, error) {
	if err := w.enc.Encode(api.Frame{Kind: api.FrameLog, Data: p}); err != nil {
		return 0, err
	}
	if w.flusher != nil {
		w.flusher.Flush()
	}
	return len(p), nil
}

// newToken returns a random opaque data-plane token.
func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *replicaServer) buildStartParams(reqBody api.ReplicaStartRequest, hostPort int) (process.StartParams, error) {
	appData, err := s.provisionAppData(reqBody.Slug)
	if err != nil {
		return process.StartParams{}, err
	}
	mounts := make([]process.SharedMount, 0, len(reqBody.SharedMountSlugs))
	for _, slug := range reqBody.SharedMountSlugs {
		// Shared sources are provisioned on this worker; resolve to the local
		// app-data path for that source slug.
		mounts = append(mounts, process.SharedMount{
			SourceSlug: slug,
			HostPath:   filepath.Join(s.dataDir, "app-data", slug),
		})
	}
	// Convert the wire env map to the KEY=VALUE slice that StartParams expects.
	env := make([]string, 0, len(reqBody.Env))
	for k, v := range reqBody.Env {
		env = append(env, k+"="+v)
	}
	return process.StartParams{
		Slug:            reqBody.Slug,
		Index:           reqBody.Index,
		Tier:            reqBody.Tier,
		Command:         reqBody.Command,
		Env:             env,
		Port:            reqBody.BindPort,
		HostPublishPort: hostPort,
		AppDataPath:     appData,
		SharedMounts:    mounts,
		MemoryLimitMB:   reqBody.MemoryLimitMB,
		CPUQuotaPercent: reqBody.CPUQuotaPercent,
		AppVersion:      reqBody.AppVersion,
		DeploymentID:    reqBody.DeploymentID,
		ContentDigest:   reqBody.ContentDigest,
	}, nil
}

func (s *replicaServer) handleStart(w http.ResponseWriter, r *http.Request) {
	var reqBody api.ReplicaStartRequest
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	hostPort := s.allocatePort()
	params, err := s.buildStartParams(reqBody, hostPort)
	if err != nil {
		http.Error(w, "provision failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	flusher, _ := w.(http.Flusher)
	logw := &frameLogWriter{enc: enc, flusher: flusher}

	endpoint, err := s.runtime.Start(r.Context(), params, logw)
	if err != nil {
		_ = enc.Encode(api.Frame{Kind: api.FrameError, Error: err.Error()})
		if flusher != nil {
			flusher.Flush()
		}
		return
	}

	token, err := newToken()
	if err != nil {
		_ = enc.Encode(api.Frame{Kind: api.FrameError, Error: err.Error()})
		if flusher != nil {
			flusher.Flush()
		}
		return
	}

	rec := &replicaRecord{
		token:       token,
		containerID: endpoint.Handle.ContainerID,
		handle:      endpoint.Handle,
		hostPort:    hostPort,
	}
	s.mu.Lock()
	s.byContainer[rec.containerID] = rec
	s.byToken[token] = rec
	s.mu.Unlock()

	result := api.ReplicaResult{
		NodeID:      s.nodeID,
		ContainerID: endpoint.Handle.ContainerID,
		URL:         fmt.Sprintf("https://%s/v1/data/%s", s.advertise, token),
	}
	resultData, _ := json.Marshal(result)
	_ = enc.Encode(api.Frame{Kind: api.FrameResult, Data: resultData})
	if flusher != nil {
		flusher.Flush()
	}

	// Keep streaming logs until the replica's output writer is closed by the
	// runtime or the request context ends. The runtime drives writes into logw
	// for its lifetime; block until the client disconnects.
	<-r.Context().Done()
}

// lookupContainer resolves a tracked replica by its worker-local container id.
func (s *replicaServer) lookupContainer(id string) (*replicaRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.byContainer[id]
	return rec, ok
}

func (s *replicaServer) handleSignal(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "container")
	rec, ok := s.lookupContainer(id)
	if !ok {
		http.Error(w, "unknown container", http.StatusNotFound)
		return
	}
	var req api.SignalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.runtime.Signal(rec.handle, syscall.Signal(req.Signal)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *replicaServer) handleStats(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "container")
	rec, ok := s.lookupContainer(id)
	if !ok {
		http.Error(w, "unknown container", http.StatusNotFound)
		return
	}
	cpu, rss, err := s.runtime.Stats(r.Context(), rec.handle)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(api.StatsResult{CPUPercent: cpu, RSSBytes: rss})
}

func (s *replicaServer) handleWait(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "container")
	rec, ok := s.lookupContainer(id)
	if !ok {
		http.Error(w, "unknown container", http.StatusNotFound)
		return
	}
	// Wait reports completion through error alone; it does not surface an exit
	// code. The caller uses this purely to detect that the replica has stopped.
	if err := s.runtime.Wait(r.Context(), rec.handle); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Replica has exited: drop it from the tables.
	s.mu.Lock()
	delete(s.byContainer, rec.containerID)
	delete(s.byToken, rec.token)
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// handleRunOnce runs a job to completion, streaming logs as NDJSON FrameLog
// frames and finishing with a FrameResult carrying the ExitResult.
func (s *replicaServer) handleRunOnce(w http.ResponseWriter, r *http.Request) {
	var reqBody api.ReplicaStartRequest
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	hostPort := s.allocatePort()
	params, err := s.buildStartParams(reqBody, hostPort)
	if err != nil {
		http.Error(w, "provision failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	flusher, _ := w.(http.Flusher)
	logw := &frameLogWriter{enc: enc, flusher: flusher}

	info, err := s.runtime.RunOnce(r.Context(), params, logw)
	if err != nil {
		_ = enc.Encode(api.Frame{Kind: api.FrameError, Error: err.Error()})
		if flusher != nil {
			flusher.Flush()
		}
		return
	}
	resultData, _ := json.Marshal(api.ExitResult{Code: info.Code, Signaled: info.Signaled})
	_ = enc.Encode(api.Frame{Kind: api.FrameResult, Data: resultData})
	if flusher != nil {
		flusher.Flush()
	}
}

// handleData reverse-proxies a data-plane request to the worker-local host port
// the replica's container is published on. The /v1/data/{token} prefix is
// stripped so the backend sees the app-relative path.
func (s *replicaServer) handleData(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	s.mu.RLock()
	rec, ok := s.byToken[token]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "unknown replica", http.StatusNotFound)
		return
	}
	if rec.hostPort == 0 {
		http.Error(w, "replica port not resolved", http.StatusServiceUnavailable)
		return
	}

	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", rec.hostPort)}
	rp := httputil.NewSingleHostReverseProxy(target)

	// Strip the /v1/data/{token} prefix; forward only the app-relative path.
	rest := chi.URLParam(r, "*")
	r.URL.Path = "/" + rest
	r.URL.RawPath = ""

	rp.ServeHTTP(w, r)
}

// portResolver is the optional runtime capability used to recover a re-adopted
// container's published host port after an agent restart. *process.DockerRuntime
// satisfies it.
type portResolver interface {
	PublishedHostPort(containerID string) (int, error)
}

// RebuildFromContainers re-adopts managed replica containers after an agent
// restart. The byContainer/byToken tables start empty on restart while managed
// containers may still be running; this enumerates them, mints a fresh
// data-plane token per replica, and resolves each container's published host
// port so inventory reports a tunnel URL and the data plane routes again.
// One-shot job containers (no replica_index label) are skipped, and any
// container already tracked is left untouched.
func (s *replicaServer) RebuildFromContainers() error {
	lister, ok := s.runtime.(containerLister)
	if !ok {
		return nil
	}
	containers, err := lister.ListByLabel(`{"label":["shinyhub.managed=true"]}`)
	if err != nil {
		return err
	}
	resolver, _ := s.runtime.(portResolver)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range containers {
		if _, isReplica := c.Labels["shinyhub.replica_index"]; !isReplica {
			continue
		}
		if _, already := s.byContainer[c.ID]; already {
			continue
		}
		token, err := newToken()
		if err != nil {
			return err
		}
		rec := &replicaRecord{token: token, containerID: c.ID}
		if resolver != nil {
			if port, err := resolver.PublishedHostPort(c.ID); err != nil {
				slog.Warn("agent: resolve published host port for re-adopted container", "container", c.ID, "err", err)
			} else {
				rec.hostPort = port
			}
		}
		s.byContainer[c.ID] = rec
		s.byToken[token] = rec
	}
	return nil
}

// Routes registers the replica-control and data-plane endpoints on r. The agent
// mounts this on its mTLS listener.
func (s *replicaServer) Routes(r chi.Router) {
	r.Get("/v1/inventory", s.handleInventory)
	r.Post("/v1/replicas", s.handleStart)
	r.Post("/v1/replicas/run-once", s.handleRunOnce)
	r.Post("/v1/replicas/{container}/signal", s.handleSignal)
	r.Get("/v1/replicas/{container}/wait", s.handleWait)
	r.Get("/v1/replicas/{container}/stats", s.handleStats)
	r.HandleFunc("/v1/data/{token}/*", s.handleData)
}

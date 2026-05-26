package worker

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"

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

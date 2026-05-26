package worker

import (
	"path/filepath"
	"sync"

	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/storage"
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

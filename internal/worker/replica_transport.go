package worker

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/rvben/shinyhub/internal/db"
)

// WorkerGetter is the minimal store interface the transport builder requires to
// resolve a worker row by node ID. *db.Store satisfies it.
type WorkerGetter interface {
	GetWorker(nodeID string) (*db.Worker, error)
}

// ReplicaTransportBuilder derives the HTTP transport for a replica row from the
// DB, without requiring the placement registry. Each instance is safe for
// concurrent use.
//
// For remote_docker replicas it builds a worker mTLS transport from the DB
// worker row (fetching via GetWorker) and caches it by worker_id so the same
// *http.Transport is reused across calls - mTLS transports are instance-
// independent (shared CA pool + rotating client cert) and safe to reuse.
//
// For all other providers (fargate, native, docker) it returns nil, directing
// the proxy to use the default HTTP transport. Fargate replicas reach their
// awsvpc private IP over plain HTTP inside the VPC; native and docker replicas
// run on localhost and require no special transport.
type ReplicaTransportBuilder struct {
	dialer AgentDialer
	store  WorkerGetter

	mu    sync.Mutex
	cache map[string]http.RoundTripper
}

// NewReplicaTransportBuilder constructs a builder that resolves transports from
// the DB rather than the placement registry, so every instance (including
// standbys whose registry is empty) can build the correct per-worker transport.
func NewReplicaTransportBuilder(dialer AgentDialer, store WorkerGetter) *ReplicaTransportBuilder {
	return &ReplicaTransportBuilder{
		dialer: dialer,
		store:  store,
		cache:  make(map[string]http.RoundTripper),
	}
}

// TransportForReplica returns the HTTP transport to use when forwarding requests
// to the given replica. It returns nil for non-remote_docker providers (the
// proxy falls back to its tuned default backend transport in that case).
//
// For remote_docker replicas the transport is built once per worker_id and
// cached; concurrent callers for the same worker_id are serialized through a
// mutex so the transport is built exactly once.
//
// The cache is unbounded and never invalidated because the cached transport
// remains correct for the lifetime of a worker entry:
//   - The client certificate rotates automatically via the dialer's
//     GetClientCertificate callback, so a cached *http.Transport always presents
//     a fresh cert on the next handshake without needing to be rebuilt.
//   - The ServerName is derived from the worker's stable node ID, which never
//     changes for a given worker row.
//   - A revoked worker never enters the cache: dialer.Transport returns an error
//     for a revoked worker (checked before insertion), and the routing layer
//     removes revoked workers from replica assignments before this function is
//     called for them.
func (b *ReplicaTransportBuilder) TransportForReplica(r *db.Replica) (http.RoundTripper, error) {
	if r.Provider != ProviderRemoteDocker {
		return nil, nil
	}

	// A remote_docker replica needs a worker store and an agent dialer to build
	// its mTLS transport. A builder constructed without them (e.g. the single-node
	// startup pool syncer when worker support is not configured) reports an error
	// here instead of dereferencing a nil dialer/store, so reconcileSlug logs the
	// error and leaves the replica unrouted rather than crashing the control plane.
	if b.store == nil {
		return nil, fmt.Errorf("transport for replica worker %q: no worker store configured", r.WorkerID)
	}
	if b.dialer == nil {
		return nil, fmt.Errorf("transport for replica worker %q: no agent dialer configured", r.WorkerID)
	}

	b.mu.Lock()
	if tr, ok := b.cache[r.WorkerID]; ok {
		b.mu.Unlock()
		return tr, nil
	}
	b.mu.Unlock()

	w, err := b.store.GetWorker(r.WorkerID)
	if err != nil {
		return nil, fmt.Errorf("transport for replica worker %q: %w", r.WorkerID, err)
	}
	if w == nil {
		return nil, fmt.Errorf("transport for replica worker %q: worker not found", r.WorkerID)
	}
	tr, err := b.dialer.Transport(*w)
	if err != nil {
		return nil, fmt.Errorf("transport for replica worker %q: %w", r.WorkerID, err)
	}

	b.mu.Lock()
	// Check again under the lock: a concurrent caller may have populated the
	// entry while we were building the transport. Prefer the cached entry to
	// avoid storing two transports for the same worker.
	if existing, ok := b.cache[r.WorkerID]; ok {
		b.mu.Unlock()
		return existing, nil
	}
	b.cache[r.WorkerID] = tr
	b.mu.Unlock()
	return tr, nil
}

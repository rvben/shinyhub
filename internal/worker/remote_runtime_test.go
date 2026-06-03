package worker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
)

// stubLookup is a test-only WorkerLookup backed by a slice. PlanPlacementForTier
// round-robins the tier's up workers (sorted by node id) across the requested
// count, so a single placement is deterministic (lowest node id) and a batch
// spreads; the load-aware greedy spread is exercised against the real
// registry/store in registry_test. Worker resolves any node by id.
type stubLookup struct {
	workers []db.Worker
}

func newStubLookup(ws ...db.Worker) *stubLookup {
	return &stubLookup{workers: ws}
}

func (s *stubLookup) PlanPlacementForTier(tier, _ string, count int) []db.Worker {
	ws := s.WorkersForTier(tier)
	if len(ws) == 0 || count <= 0 {
		return nil
	}
	out := make([]db.Worker, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, ws[i%len(ws)])
	}
	return out
}

func (s *stubLookup) WorkersForTier(tier string) []db.Worker {
	var out []db.Worker
	for _, w := range s.workers {
		if w.Tier == tier && w.Status == "up" {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

func (s *stubLookup) Worker(nodeID string) (db.Worker, bool) {
	for _, w := range s.workers {
		if w.NodeID == nodeID {
			return w, true
		}
	}
	return db.Worker{}, false
}

// TestToStartRequest_IncludesSecretEnv guards that secret env vars reach a
// remote worker: a remote_docker container shares the worker's trust boundary
// like local Docker, so SecretEnv must be folded into the wire env map
// alongside Env (no behavior change vs a single flat Env slice).
func TestToStartRequest_IncludesSecretEnv(t *testing.T) {
	req := toStartRequest(process.StartParams{
		Slug:      "demo",
		Env:       []string{"PLAIN=1"},
		SecretEnv: []string{"SECRET=shh"},
	})
	if req.Env["PLAIN"] != "1" {
		t.Errorf("PLAIN missing from wire env: %v", req.Env)
	}
	if req.Env["SECRET"] != "shh" {
		t.Errorf("SECRET (from SecretEnv) missing from wire env: %v", req.Env)
	}
}

func TestRemoteRuntime_NoWorker_FailsClosed(t *testing.T) {
	lookup := newStubLookup() // empty: no workers for any tier
	rt := newRemoteRuntime(lookup, "remote", nil)

	_, err := rt.Start(context.Background(), process.StartParams{Slug: "app", Port: 8080}, nil)
	if err == nil {
		t.Fatal("Start with no live worker: want error, got nil")
	}
	if !strings.Contains(err.Error(), "no live worker") {
		t.Errorf("error = %q, want it to mention no live worker", err.Error())
	}
}

// TestRemoteRuntime_NoWorker_WrapsSentinel verifies the no-live-worker failure
// wraps process.ErrNoLiveWorker, so the watcher's restart path can classify it
// as a zero-cost failure via errors.Is rather than burning the restart budget.
func TestRemoteRuntime_NoWorker_WrapsSentinel(t *testing.T) {
	rt := newRemoteRuntime(newStubLookup(), "remote", nil)
	_, err := rt.Start(context.Background(), process.StartParams{Slug: "app", Port: 8080}, nil)
	if !errors.Is(err, process.ErrNoLiveWorker) {
		t.Fatalf("error = %v, want errors.Is(err, process.ErrNoLiveWorker)", err)
	}
}

func TestRemoteRuntime_Capabilities(t *testing.T) {
	rt := newRemoteRuntime(newStubLookup(), "remote", nil)
	if rt.HostProvidesAppData() {
		t.Error("remoteRuntime.HostProvidesAppData() = true, want false")
	}
	if rt.HostPreparesDeps() {
		t.Error("remoteRuntime.HostPreparesDeps() = true, want false")
	}
	// Implements ReplicaTransporter.
	var _ process.ReplicaTransporter = rt
}

func TestRemoteRuntime_HandleValidation(t *testing.T) {
	lookup := newStubLookup(db.Worker{NodeID: "node-a", Tier: "remote", AdvertiseAddr: "w:8443", Status: "up"})
	rt := newRemoteRuntime(lookup, "remote", nil)

	// A handle whose node prefix does not match any resolvable worker must
	// be rejected before any dial.
	err := rt.Signal(process.RunHandle{ContainerID: "other-node/c-1"}, syscall.SIGTERM)
	if err == nil {
		t.Fatal("Signal with mismatched node handle: want error")
	}
}

// captureDialer records the node id of the worker it was asked to dial.
type captureDialer struct {
	client *http.Client
	base   string
	dialed *string
}

func (d *captureDialer) DialWorker(w db.Worker) (*http.Client, string, error) {
	*d.dialed = w.NodeID
	return d.client, d.base, nil
}
func (d *captureDialer) Transport(db.Worker) (http.RoundTripper, error) {
	return d.client.Transport, nil
}

// TestRemoteRuntime_HandleResolvesByOwningNode asserts a handle is routed to the
// worker that owns it, identified by the handle's encoded node id, even when that
// worker is not the tier's first (routing) worker. Under multi-worker placement a
// replica can live on any up worker on the tier, so signal/wait/stats must follow
// the handle's node rather than assuming the tier's single live worker.
func TestRemoteRuntime_HandleResolvesByOwningNode(t *testing.T) {
	var dialed string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	// node-a sorts first, so it is WorkerForTier; the handle is owned by node-b.
	lookup := newStubLookup(
		db.Worker{NodeID: "node-a", Tier: "remote", AdvertiseAddr: "a:8443", Status: "up"},
		db.Worker{NodeID: "node-b", Tier: "remote", AdvertiseAddr: "b:8443", Status: "up"},
	)
	rt := newRemoteRuntime(lookup, "remote", &captureDialer{client: srv.Client(), base: srv.URL, dialed: &dialed})

	if err := rt.Signal(process.RunHandle{ContainerID: "node-b/c-1"}, syscall.SIGTERM); err != nil {
		t.Fatalf("Signal for node-b handle: %v", err)
	}
	if dialed != "node-b" {
		t.Fatalf("dialed worker = %q, want node-b", dialed)
	}
}

// TestRemoteRuntime_HandleForOtherTierFailsClosed asserts that a handle owned by
// an up worker on a different tier than this runtime is rejected before any dial.
// A stale or mismatched manager/recovery entry must never let one tier's runtime
// signal, wait on, or sample another tier's worker; it fails closed instead.
func TestRemoteRuntime_HandleForOtherTierFailsClosed(t *testing.T) {
	lookup := newStubLookup(
		db.Worker{NodeID: "node-a", Tier: "remote-a", AdvertiseAddr: "a:8443", Status: "up"},
		db.Worker{NodeID: "node-b", Tier: "remote-b", AdvertiseAddr: "b:8443", Status: "up"},
	)
	rt := newRemoteRuntime(lookup, "remote-a", nil)
	if err := rt.Signal(process.RunHandle{ContainerID: "node-b/c-1"}, syscall.SIGTERM); err == nil {
		t.Fatal("Signal for handle owned by another tier's worker: want error")
	}
}

// TestRemoteRuntime_HandleForDownWorkerFailsClosed asserts that a handle owned by
// a worker that is no longer up is rejected before any dial: a dial to a down
// worker would hang or fail, so the runtime fails closed.
func TestRemoteRuntime_HandleForDownWorkerFailsClosed(t *testing.T) {
	lookup := newStubLookup(
		db.Worker{NodeID: "node-a", Tier: "remote", AdvertiseAddr: "a:8443", Status: "up"},
		db.Worker{NodeID: "node-b", Tier: "remote", AdvertiseAddr: "b:8443", Status: "down"},
	)
	rt := newRemoteRuntime(lookup, "remote", nil)
	if err := rt.Signal(process.RunHandle{ContainerID: "node-b/c-1"}, syscall.SIGTERM); err == nil {
		t.Fatal("Signal for handle owned by a down worker: want error")
	}
}

// roundTripStub is a named, comparable http.RoundTripper so tests can assert
// identity of the transport that was resolved.
type roundTripStub string

func (roundTripStub) RoundTrip(*http.Request) (*http.Response, error) { return nil, nil }

// transportProbeDialer hands back a distinct RoundTripper per worker so a test
// can assert which worker's transport was resolved.
type transportProbeDialer struct {
	byNode map[string]http.RoundTripper
}

func (d *transportProbeDialer) DialWorker(db.Worker) (*http.Client, string, error) {
	return nil, "", nil
}
func (d *transportProbeDialer) Transport(w db.Worker) (http.RoundTripper, error) {
	return d.byNode[w.NodeID], nil
}

// TestRemoteRuntime_ReplicaTransportForWorker asserts that the per-worker
// transport resolves to the named worker's mTLS transport, and fails closed
// (nil) for a worker that is unknown, down, or on a different tier - so a route
// is never built with the wrong worker's transport.
func TestRemoteRuntime_ReplicaTransportForWorker(t *testing.T) {
	trA := roundTripStub("a")
	trB := roundTripStub("b")
	lookup := newStubLookup(
		db.Worker{NodeID: "node-a", Tier: "remote", AdvertiseAddr: "a:8443", Status: "up"},
		db.Worker{NodeID: "node-b", Tier: "remote", AdvertiseAddr: "b:8443", Status: "up"},
		db.Worker{NodeID: "node-down", Tier: "remote", AdvertiseAddr: "d:8443", Status: "down"},
		db.Worker{NodeID: "node-other", Tier: "elsewhere", AdvertiseAddr: "e:8443", Status: "up"},
	)
	dialer := &transportProbeDialer{byNode: map[string]http.RoundTripper{
		"node-a": trA, "node-b": trB,
	}}
	rt := newRemoteRuntime(lookup, "remote", dialer)

	if got := rt.ReplicaTransportForWorker("node-b"); got != trB {
		t.Errorf("ReplicaTransportForWorker(node-b) = %v, want node-b's transport", got)
	}
	if got := rt.ReplicaTransportForWorker("node-a"); got != trA {
		t.Errorf("ReplicaTransportForWorker(node-a) = %v, want node-a's transport", got)
	}
	if got := rt.ReplicaTransportForWorker("node-down"); got != nil {
		t.Errorf("ReplicaTransportForWorker(node-down) = %v, want nil (down)", got)
	}
	if got := rt.ReplicaTransportForWorker("node-other"); got != nil {
		t.Errorf("ReplicaTransportForWorker(node-other) = %v, want nil (other tier)", got)
	}
	if got := rt.ReplicaTransportForWorker("node-missing"); got != nil {
		t.Errorf("ReplicaTransportForWorker(node-missing) = %v, want nil (unknown)", got)
	}
}

func TestEncodeDecodeRemoteHandle(t *testing.T) {
	h := encodeRemoteHandle("node-a", "c-1")
	if h != "node-a/c-1" {
		t.Errorf("encodeRemoteHandle = %q, want node-a/c-1", h)
	}
	node, container, err := decodeRemoteHandle("node-a/c-1")
	if err != nil || node != "node-a" || container != "c-1" {
		t.Fatalf("decodeRemoteHandle = (%q,%q,%v)", node, container, err)
	}
	if _, _, err := decodeRemoteHandle("nostructure"); err == nil {
		t.Error("decodeRemoteHandle of malformed handle: want error")
	}
}

func TestRemoteRuntime_StartAgainstRealAgentServer(t *testing.T) {
	dir := t.TempDir()
	agentSrv := NewReplicaServer(ReplicaServerConfig{
		Runtime:      &fakeRuntime{startURL: "http://127.0.0.1:49001"},
		DataDir:      dir,
		NodeID:       "node-a",
		Advertise:    "w:8443",
		AllocatePort: func() int { return 49001 },
	})
	router := chi.NewRouter()
	agentSrv.Routes(router)
	ts := httptest.NewServer(router)
	// Close connections before waiting for the server to shut down: the agent
	// Start handler keeps the connection open after writing the result frame so
	// the control plane can continue to receive log frames. Closing client
	// connections first unblocks both the server handler and the drain goroutine.
	defer func() {
		ts.CloseClientConnections()
		ts.Close()
	}()

	lookup := newStubLookup(db.Worker{NodeID: "node-a", Tier: "remote", AdvertiseAddr: "w:8443", Status: "up"})
	rt := newRemoteRuntime(lookup, "remote", &stubDialer{client: ts.Client(), base: ts.URL})

	var logs strings.Builder
	ep, err := rt.Start(context.Background(), process.StartParams{
		Slug: "app", Port: 8080, Command: []string{"./server"},
	}, &logs)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !strings.HasPrefix(ep.URL, "https://w:8443/v1/data/") {
		t.Errorf("endpoint URL = %q, want tunnel URL", ep.URL)
	}
	// Handle is the opaque node/container form.
	if !strings.HasPrefix(ep.Handle.ContainerID, "node-a/") {
		t.Errorf("handle = %q, want node-a/ prefix", ep.Handle.ContainerID)
	}
}

// TestRemoteRuntime_StartHonorsTargetWorker asserts that when StartParams pins a
// pre-planned target worker, Start dials exactly that worker rather than
// self-placing onto the tier's first worker. Deploy relies on this to make a
// pre-planned pool spread actually land where it planned.
func TestRemoteRuntime_StartHonorsTargetWorker(t *testing.T) {
	dir := t.TempDir()
	agentSrv := NewReplicaServer(ReplicaServerConfig{
		Runtime:      &fakeRuntime{startURL: "http://127.0.0.1:49011"},
		DataDir:      dir,
		NodeID:       "node-b",
		Advertise:    "b:8443",
		AllocatePort: func() int { return 49011 },
	})
	router := chi.NewRouter()
	agentSrv.Routes(router)
	ts := httptest.NewServer(router)
	defer func() { ts.CloseClientConnections(); ts.Close() }()

	// node-a sorts first (self-placement would pick it); the target pins node-b.
	var dialed string
	lookup := newStubLookup(
		db.Worker{NodeID: "node-a", Tier: "remote", AdvertiseAddr: "a:8443", Status: "up"},
		db.Worker{NodeID: "node-b", Tier: "remote", AdvertiseAddr: "b:8443", Status: "up"},
	)
	rt := newRemoteRuntime(lookup, "remote", &captureDialer{client: ts.Client(), base: ts.URL, dialed: &dialed})

	_, err := rt.Start(context.Background(), process.StartParams{
		Slug: "app", Port: 8080, Command: []string{"./server"}, TargetWorker: "node-b",
	}, nil)
	if err != nil {
		t.Fatalf("Start with target node-b: %v", err)
	}
	if dialed != "node-b" {
		t.Fatalf("dialed worker = %q, want pinned target node-b", dialed)
	}
}

// TestRemoteRuntime_StartRejectsDownTargetWorker asserts that a pre-planned
// target that is no longer up fails closed rather than dialing a dead worker or
// silently falling back to another worker.
func TestRemoteRuntime_StartRejectsDownTargetWorker(t *testing.T) {
	lookup := newStubLookup(
		db.Worker{NodeID: "node-a", Tier: "remote", AdvertiseAddr: "a:8443", Status: "up"},
		db.Worker{NodeID: "node-b", Tier: "remote", AdvertiseAddr: "b:8443", Status: "down"},
	)
	rt := newRemoteRuntime(lookup, "remote", nil)
	_, err := rt.Start(context.Background(), process.StartParams{
		Slug: "app", Port: 8080, Command: []string{"./server"}, TargetWorker: "node-b",
	}, nil)
	if !errors.Is(err, process.ErrNoLiveWorker) {
		t.Fatalf("Start with down target = %v, want errors.Is(err, ErrNoLiveWorker)", err)
	}
}

type stubDialer struct {
	client *http.Client
	base   string
}

func (d *stubDialer) DialWorker(db.Worker) (*http.Client, string, error) {
	return d.client, d.base, nil
}
func (d *stubDialer) Transport(db.Worker) (http.RoundTripper, error) {
	return d.client.Transport, nil
}

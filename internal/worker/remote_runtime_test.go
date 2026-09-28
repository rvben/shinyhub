package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/worker/api"
)

// stubLookup is a test-only WorkerLookup backed by a slice. PlanPlacementForTier
// round-robins the tier's up workers (sorted by node id) across the requested
// count, so a single placement is deterministic (lowest node id) and a batch
// spreads; the load-aware greedy spread is exercised against the real
// registry/store in registry_test. Worker resolves any node by id. Reads are
// mutex-guarded so a test can demote a worker (setStatus) while a runtime call
// is in flight, the way the heartbeat monitor does in production.
type stubLookup struct {
	mu      sync.RWMutex
	workers []db.Worker
}

func newStubLookup(ws ...db.Worker) *stubLookup {
	return &stubLookup{workers: ws}
}

// setStatus changes a worker's registry status, standing in for the heartbeat
// down-sweep marking a silent worker down.
func (s *stubLookup) setStatus(nodeID, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.workers {
		if s.workers[i].NodeID == nodeID {
			s.workers[i].Status = status
		}
	}
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
	s.mu.RLock()
	defer s.mu.RUnlock()
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
	s.mu.RLock()
	defer s.mu.RUnlock()
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

// refusingTransport fails the first failsLeft round trips with a connection-
// refused dial error, then delegates to inner. It models a worker dialed in the
// brief window after it joined but before its data-plane listener bound.
type refusingTransport struct {
	mu        sync.Mutex
	failsLeft int
	attempts  int
	inner     http.RoundTripper
}

func (f *refusingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.attempts++
	refuse := f.failsLeft > 0
	if refuse {
		f.failsLeft--
	}
	f.mu.Unlock()
	if refuse {
		// Same shape as a real connect refusal so errors.Is(err, ECONNREFUSED)
		// holds through the *url.Error http.Client wraps around it.
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	}
	return f.inner.RoundTrip(req)
}

func (f *refusingTransport) attemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

// TestRemoteRuntime_StartRetriesOnConnectionRefused asserts Start retries a
// connection-refused POST (the request never reached the worker, so a re-POST
// cannot double-start a container) and succeeds once the worker's data-plane
// listener is accepting. This is the defense-in-depth half of the worker-
// readiness fix: even if a deploy races a freshly-joined worker's listener bind,
// the short retry rides over it instead of failing the whole deploy.
func TestRemoteRuntime_StartRetriesOnConnectionRefused(t *testing.T) {
	dir := t.TempDir()
	agentSrv := NewReplicaServer(ReplicaServerConfig{
		Runtime:      &fakeRuntime{startURL: "http://127.0.0.1:49021"},
		DataDir:      dir,
		NodeID:       "node-a",
		Advertise:    "w:8443",
		AllocatePort: func() int { return 49021 },
	})
	router := chi.NewRouter()
	agentSrv.Routes(router)
	ts := httptest.NewServer(router)
	defer func() { ts.CloseClientConnections(); ts.Close() }()

	ft := &refusingTransport{failsLeft: 2, inner: ts.Client().Transport}
	rt := newRemoteRuntime(
		newStubLookup(db.Worker{NodeID: "node-a", Tier: "remote", AdvertiseAddr: "w:8443", Status: "up"}),
		"remote",
		&stubDialer{client: &http.Client{Transport: ft}, base: ts.URL},
	)

	ep, err := rt.Start(context.Background(), process.StartParams{
		Slug: "app", Port: 8080, Command: []string{"./server"},
	}, nil)
	if err != nil {
		t.Fatalf("Start should retry past connection-refused, got: %v", err)
	}
	if got := ft.attemptCount(); got != 3 {
		t.Fatalf("Start made %d attempts, want 3 (2 refused + 1 success)", got)
	}
	if !strings.HasPrefix(ep.URL, "https://w:8443/v1/data/") {
		t.Errorf("endpoint URL = %q, want tunnel URL", ep.URL)
	}
}

// unreachableTransport fails every round trip with a connection-refused dial
// error. It models a worker whose process is gone while its registry row still
// says "up": the heartbeat timeout has not elapsed, so nothing yet knows whether
// the replica it hosts is alive.
type unreachableTransport struct{}

func (unreachableTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
}

// TestRemoteRuntime_WaitHoldsWhileWorkerUnreachable asserts an unreachable
// worker is never reported as a replica exit. The Manager calls Wait purely to
// detect exit and reads any return as "this replica is gone", so returning the
// moment a worker stops answering fabricates a crash - with an invented exit
// reason and a spent restart budget - for a container that may still be running.
// Reachability is unknown, not zero: Wait must hold it and defer to the
// heartbeat monitor, which is the single authority on worker liveness, and only
// report the loss once that monitor has ruled.
func TestRemoteRuntime_WaitHoldsWhileWorkerUnreachable(t *testing.T) {
	lookup := newStubLookup(db.Worker{NodeID: "node-a", Tier: "remote", AdvertiseAddr: "w:8443", Status: "up"})
	rt := newRemoteRuntime(lookup, "remote",
		&stubDialer{client: &http.Client{Transport: unreachableTransport{}}, base: "https://w:8443"})
	rt.waitRetry = time.Millisecond

	done := make(chan error, 1)
	go func() {
		done <- rt.Wait(context.Background(), process.RunHandle{ContainerID: encodeRemoteHandle("node-a", "c1")})
	}()

	select {
	case err := <-done:
		t.Fatalf("Wait returned (%v) while the worker was merely unreachable; the Manager reads that as a replica exit", err)
	case <-time.After(100 * time.Millisecond):
	}

	// The heartbeat monitor marks the silent worker down. Now - and only now -
	// the replica is known lost, which Wait reports as ErrNoLiveWorker so the
	// Manager can distinguish it from an exit.
	lookup.setStatus("node-a", "down")
	select {
	case err := <-done:
		if !errors.Is(err, process.ErrNoLiveWorker) {
			t.Fatalf("Wait after the worker was marked down = %v, want errors.Is(err, process.ErrNoLiveWorker)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after the worker was marked down")
	}
}

// TestRemoteRuntime_WaitReturnsOnReplicaExit is the positive control for the
// test above: a worker that answers still ends the wait promptly and exactly
// once, so holding an unreachable worker open costs nothing on the real exit
// path.
func TestRemoteRuntime_WaitReturnsOnReplicaExit(t *testing.T) {
	var mu sync.Mutex
	var hits int
	router := chi.NewRouter()
	router.Get("/v1/replicas/{id}/wait", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	ts := httptest.NewServer(router)
	defer func() { ts.CloseClientConnections(); ts.Close() }()

	rt := newRemoteRuntime(
		newStubLookup(db.Worker{NodeID: "node-a", Tier: "remote", AdvertiseAddr: "w:8443", Status: "up"}),
		"remote",
		&stubDialer{client: ts.Client(), base: ts.URL},
	)
	rt.waitRetry = time.Millisecond

	done := make(chan error, 1)
	go func() {
		done <- rt.Wait(context.Background(), process.RunHandle{ContainerID: encodeRemoteHandle("node-a", "c1")})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait on a reported exit = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return on a reported replica exit")
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Fatalf("worker wait endpoint hit %d times, want 1", hits)
	}
}

// TestRemoteRuntime_StartDoesNotRetryWorkerError asserts Start fails fast on a
// non-dial error: a worker HTTP status means the POST reached the worker, so a
// retry could double-start a container. Only connection-establishment failures
// are safe to retry.
func TestRemoteRuntime_StartDoesNotRetryWorkerError(t *testing.T) {
	var hits int
	var mu sync.Mutex
	router := chi.NewRouter()
	router.Post("/v1/replicas", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	})
	ts := httptest.NewServer(router)
	defer func() { ts.CloseClientConnections(); ts.Close() }()

	rt := newRemoteRuntime(
		newStubLookup(db.Worker{NodeID: "node-a", Tier: "remote", AdvertiseAddr: "w:8443", Status: "up"}),
		"remote",
		&stubDialer{client: ts.Client(), base: ts.URL},
	)

	if _, err := rt.Start(context.Background(), process.StartParams{
		Slug: "app", Port: 8080, Command: []string{"./server"},
	}, nil); err == nil {
		t.Fatal("Start against a 500 worker should error")
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Fatalf("worker hit %d times, want 1 (no retry on a worker HTTP error)", hits)
	}
}

// slowExitRuntime wraps fakeRuntime, delaying Wait's return so a test can
// drive a real replicaServer against a replica that stays alive far longer
// than the client's response-header timeout.
type slowExitRuntime struct {
	*fakeRuntime
	delay   time.Duration
	waitErr error
}

func (r *slowExitRuntime) Wait(ctx context.Context, _ process.RunHandle) error {
	select {
	case <-time.After(r.delay):
		return r.waitErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// countingTransport counts round trips so a test can assert a single request
// was made rather than the retry/re-dial churn a broken protocol would cause.
type countingTransport struct {
	http.RoundTripper
	mu   sync.Mutex
	hits int
}

func (c *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.hits++
	c.mu.Unlock()
	return c.RoundTripper.RoundTrip(req)
}

func (c *countingTransport) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits
}

// newWaitTestServer starts a real replicaServer with one tracked replica
// ("c-1") backed by rt, and returns it alongside the server to close.
func newWaitTestServer(t *testing.T, rt process.Runtime) *httptest.Server {
	t.Helper()
	agentSrv := NewReplicaServer(ReplicaServerConfig{
		Runtime: rt, DataDir: t.TempDir(), NodeID: "node-a", Advertise: "w:8443",
	})
	agentSrv.mu.Lock()
	rec := &replicaRecord{token: "tok", containerID: "c-1"}
	agentSrv.byContainer["c-1"] = rec
	agentSrv.byToken["tok"] = rec
	agentSrv.mu.Unlock()
	router := chi.NewRouter()
	agentSrv.Routes(router)
	ts := httptest.NewServer(router)
	t.Cleanup(func() { ts.CloseClientConnections(); ts.Close() })
	return ts
}

// TestRemoteRuntime_WaitSurvivesShortResponseHeaderTimeout asserts that a
// replica which stays alive far longer than the client's response-header
// timeout is not mistaken for an unreachable worker. The worker flushes
// headers immediately (200) and reports the outcome afterwards in the body,
// so the header timeout bounds only "did the worker answer", never "how long
// does the replica run". Before the fix, the worker wrote its answer (204)
// only once Wait returned, so every real wait longer than the timeout tripped
// it and the client re-dialled forever without the replica ever exiting.
func TestRemoteRuntime_WaitSurvivesShortResponseHeaderTimeout(t *testing.T) {
	ts := newWaitTestServer(t, &slowExitRuntime{fakeRuntime: &fakeRuntime{}, delay: 150 * time.Millisecond})

	transport := &countingTransport{RoundTripper: &http.Transport{ResponseHeaderTimeout: 20 * time.Millisecond}}
	rt := newRemoteRuntime(
		newStubLookup(db.Worker{NodeID: "node-a", Tier: "remote", AdvertiseAddr: "w:8443", Status: "up"}),
		"remote",
		&stubDialer{client: &http.Client{Transport: transport}, base: ts.URL},
	)
	rt.waitRetry = time.Millisecond

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- rt.Wait(context.Background(), process.RunHandle{ContainerID: encodeRemoteHandle("node-a", "c-1")})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return within 2s: a response-header timeout shorter than the replica's lifetime is blocking it, which is the bug under test")
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("Wait returned after %v, faster than the replica's own exit delay: it did not actually wait for the exit", elapsed)
	}
	if hits := transport.count(); hits != 1 {
		t.Fatalf("worker wait endpoint hit %d times, want 1 (no re-dial churn against a live replica)", hits)
	}
}

// TestRemoteRuntime_WaitReportsWorkerFailureWithoutRetry asserts that once a
// current-protocol worker has flushed its 200 response headers, an explicit
// error the worker reports afterward (a FrameError frame) is treated as a
// final answer - not retried like a transport failure, and not silently
// reduced to a generic "worker wait returned %d" the way a non-2xx status
// used to be, since headers are already committed to 200 by the time the
// worker knows the outcome.
func TestRemoteRuntime_WaitReportsWorkerFailureWithoutRetry(t *testing.T) {
	ts := newWaitTestServer(t, &slowExitRuntime{fakeRuntime: &fakeRuntime{}, waitErr: errors.New("boom")})

	transport := &countingTransport{RoundTripper: &http.Transport{}}
	rt := newRemoteRuntime(
		newStubLookup(db.Worker{NodeID: "node-a", Tier: "remote", AdvertiseAddr: "w:8443", Status: "up"}),
		"remote",
		&stubDialer{client: &http.Client{Transport: transport}, base: ts.URL},
	)
	rt.waitRetry = time.Millisecond

	done := make(chan error, 1)
	go func() {
		done <- rt.Wait(context.Background(), process.RunHandle{ContainerID: encodeRemoteHandle("node-a", "c-1")})
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("Wait = %v, want an error containing the worker's reported failure (\"boom\")", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return within 2s: a worker-reported failure must be final, not retried")
	}
	if hits := transport.count(); hits != 1 {
		t.Fatalf("worker wait endpoint hit %d times, want 1 (a worker-reported failure must not be retried)", hits)
	}
}

// TestRemoteRuntime_WaitPreservesProcessExitErrorAcrossTheWire asserts that a
// genuine *process.ProcessExitError from the worker's local Wait survives the
// NDJSON round trip: the control plane's Wait must be able to prove the exit
// itself (errors.As(err, *process.ProcessExitError)), not merely see an
// opaque worker-reported failure string. A caller that cannot recover the
// typed error treats a real exit the same as a transport failure that proves
// nothing, which is exactly the ambiguity exitObserved exists to remove.
func TestRemoteRuntime_WaitPreservesProcessExitErrorAcrossTheWire(t *testing.T) {
	ts := newWaitTestServer(t, &slowExitRuntime{fakeRuntime: &fakeRuntime{}, waitErr: &process.ProcessExitError{Code: 7}})

	transport := &countingTransport{RoundTripper: &http.Transport{}}
	rt := newRemoteRuntime(
		newStubLookup(db.Worker{NodeID: "node-a", Tier: "remote", AdvertiseAddr: "w:8443", Status: "up"}),
		"remote",
		&stubDialer{client: &http.Client{Transport: transport}, base: ts.URL},
	)
	rt.waitRetry = time.Millisecond

	done := make(chan error, 1)
	go func() {
		done <- rt.Wait(context.Background(), process.RunHandle{ContainerID: encodeRemoteHandle("node-a", "c-1")})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Wait on a reported exit = nil, want the worker's ProcessExitError")
		}
		var exitErr *process.ProcessExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("Wait = %v, want errors.As to recover a *process.ProcessExitError; the exit proof was lost crossing the wire", err)
		}
		if exitErr.Code != 7 {
			t.Fatalf("recovered ProcessExitError.Code = %d, want 7", exitErr.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return within 2s")
	}
	if hits := transport.count(); hits != 1 {
		t.Fatalf("worker wait endpoint hit %d times, want 1", hits)
	}
}

// TestRemoteRuntime_WaitPreservesSignalAcrossTheWire asserts that a
// *process.ProcessExitError carrying a Signal (a native replica killed by a
// signal rather than exiting with a code) survives the same NDJSON round
// trip: the control plane's Wait must recover both Code and Signal, not just
// Code. Losing Signal makes a signal-killed remote replica indistinguishable
// from an ordinary exit code -1, discarding the crash reason the local
// runtime already preserves.
func TestRemoteRuntime_WaitPreservesSignalAcrossTheWire(t *testing.T) {
	ts := newWaitTestServer(t, &slowExitRuntime{fakeRuntime: &fakeRuntime{}, waitErr: &process.ProcessExitError{Code: -1, Signal: syscall.SIGKILL}})

	transport := &countingTransport{RoundTripper: &http.Transport{}}
	rt := newRemoteRuntime(
		newStubLookup(db.Worker{NodeID: "node-a", Tier: "remote", AdvertiseAddr: "w:8443", Status: "up"}),
		"remote",
		&stubDialer{client: &http.Client{Transport: transport}, base: ts.URL},
	)
	rt.waitRetry = time.Millisecond

	done := make(chan error, 1)
	go func() {
		done <- rt.Wait(context.Background(), process.RunHandle{ContainerID: encodeRemoteHandle("node-a", "c-1")})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Wait on a signal-killed exit = nil, want the worker's ProcessExitError")
		}
		var exitErr *process.ProcessExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("Wait = %v, want errors.As to recover a *process.ProcessExitError; the exit proof was lost crossing the wire", err)
		}
		if exitErr.Signal != syscall.SIGKILL {
			t.Fatalf("recovered ProcessExitError.Signal = %v, want SIGKILL: the signal was dropped crossing the worker wire", exitErr.Signal)
		}
		if exitErr.Code != -1 {
			t.Fatalf("recovered ProcessExitError.Code = %d, want -1", exitErr.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return within 2s")
	}
	if hits := transport.count(); hits != 1 {
		t.Fatalf("worker wait endpoint hit %d times, want 1", hits)
	}
}

// TestReplicaServer_WaitKeepsLegacyAnswerForClientsThatDoNotOptIn asserts
// that a control plane which does not ask for the streamed wait response
// still gets the original contract: no response at all until the replica
// exits, then 204. An older control plane treats any 200 from this endpoint
// as "the replica has exited", so answering it with an immediate 200 would
// mark every live replica stopped during a rolling upgrade where the worker
// is upgraded first.
func TestReplicaServer_WaitKeepsLegacyAnswerForClientsThatDoNotOptIn(t *testing.T) {
	ts := newWaitTestServer(t, &slowExitRuntime{fakeRuntime: &fakeRuntime{}, delay: 150 * time.Millisecond})

	start := time.Now()
	resp, err := http.Get(ts.URL + "/v1/replicas/c-1/wait")
	if err != nil {
		t.Fatalf("wait request: %v", err)
	}
	elapsed := time.Since(start)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("legacy wait status = %d, want 204 (an older control plane reads 200 as an exit)", resp.StatusCode)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("legacy wait answered after %v, before the replica's %v exit delay", elapsed, 150*time.Millisecond)
	}
}

// TestRemoteRuntime_WaitTreatsALegacyWorkerFailureAsExit pins compatibility
// with a worker that predates exit reporting. Such a worker streams a bare
// FrameError when its Wait returns with an error, carrying neither an exit
// code nor the exit_proven marker. Its Wait returning was always the signal
// that the replica stopped, so the control plane must keep reading it that
// way; otherwise a stop through an older worker can never be confirmed and
// the replica's slot stays fenced until the worker is upgraded. A current
// worker's failure that it does not mark as an exit proves nothing.
func TestRemoteRuntime_WaitTreatsALegacyWorkerFailureAsExit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		frame    string
		wantExit bool
	}{
		{name: "legacy worker", frame: `{"kind":"error","error":"wait: exit status 1"}`, wantExit: true},
		{name: "current worker, unproven", frame: `{"kind":"error","error":"wait: boom","exit_proven":false}`, wantExit: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", waitStreamContentType)
				w.WriteHeader(http.StatusOK)
				io.WriteString(w, tc.frame+"\n")
			}))
			t.Cleanup(ts.Close)
			rt := newRemoteRuntime(
				newStubLookup(db.Worker{NodeID: "node-a", Tier: "remote", AdvertiseAddr: "w:8443", Status: "up"}),
				"remote",
				&stubDialer{client: ts.Client(), base: ts.URL},
			)
			rt.waitRetry = time.Millisecond

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err := rt.Wait(ctx, process.RunHandle{ContainerID: encodeRemoteHandle("node-a", "c-1")})
			if err == nil {
				t.Fatal("Wait on a worker-reported failure = nil")
			}
			var exitErr *process.ProcessExitError
			if got := errors.As(err, &exitErr); got != tc.wantExit {
				t.Fatalf("Wait = %v: exit proven = %v, want %v", err, got, tc.wantExit)
			}
		})
	}
}

// TestReplicaServer_WaitFrameMarksExitProof pins the worker's side of the
// same contract: every FrameError from the streamed wait states whether it
// proves an exit, so the control plane can tell it apart from an older
// worker that says nothing.
func TestReplicaServer_WaitFrameMarksExitProof(t *testing.T) {
	for _, tc := range []struct {
		name    string
		waitErr error
		want    bool
	}{
		{name: "exit", waitErr: &process.ProcessExitError{Code: 3}, want: true},
		{name: "other failure", waitErr: errors.New("boom"), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := newWaitTestServer(t, &slowExitRuntime{fakeRuntime: &fakeRuntime{}, waitErr: tc.waitErr})
			req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/replicas/c-1/wait", nil)
			req.Header.Set("Accept", waitStreamContentType)
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var fr api.Frame
			if err := json.NewDecoder(resp.Body).Decode(&fr); err != nil {
				t.Fatalf("decode frame: %v", err)
			}
			if fr.Kind != api.FrameError {
				t.Fatalf("frame kind = %q, want error", fr.Kind)
			}
			if fr.ExitProven == nil || *fr.ExitProven != tc.want {
				t.Fatalf("exit_proven = %v, want %v", fr.ExitProven, tc.want)
			}
		})
	}
}

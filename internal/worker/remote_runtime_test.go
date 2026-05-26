package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
)

// stubLookup is a test-only WorkerLookup backed by an in-memory map.
type stubLookup struct {
	workers map[string]db.Worker // keyed by tier
}

func newStubLookup(ws ...db.Worker) *stubLookup {
	m := map[string]db.Worker{}
	for _, w := range ws {
		m[w.Tier] = w
	}
	return &stubLookup{workers: m}
}

func (s *stubLookup) WorkerForTier(tier string) (db.Worker, bool) {
	w, ok := s.workers[tier]
	return w, ok
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

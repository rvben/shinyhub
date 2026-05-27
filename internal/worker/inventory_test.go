package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/worker/api"
)

// fakeLister is a fakeRuntime that also lists containers and resolves published
// ports, standing in for the worker DockerRuntime during inventory and rebuild tests.
type fakeLister struct {
	fakeRuntime
	containers []process.ContainerInfo
	hostPorts  map[string]int
}

func (f *fakeLister) ListByLabel(string) ([]process.ContainerInfo, error) { return f.containers, nil }
func (f *fakeLister) PublishedHostPort(id string) (int, error)            { return f.hostPorts[id], nil }

func TestReplicaServer_Inventory_ReturnsLabelsAndTunnelURL(t *testing.T) {
	dir := t.TempDir()
	rt := &fakeLister{containers: []process.ContainerInfo{
		{ID: "c-1", Labels: map[string]string{
			"shinyhub.slug": "app", "shinyhub.replica_index": "0",
			"shinyhub.deployment_id": "7",
		}},
	}}
	srv := NewReplicaServer(ReplicaServerConfig{
		Runtime: rt, DataDir: dir, NodeID: "node-a", Advertise: "w:8443",
		AllocatePort: func() int { return 49001 },
	})
	srv.mu.Lock()
	record := &replicaRecord{token: "tok", containerID: "c-1", hostPort: 49001}
	srv.byContainer["c-1"] = record
	srv.byToken["tok"] = record
	srv.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/v1/inventory", nil)
	rec := httptest.NewRecorder()
	srv.handleInventory(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var items []api.InventoryItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if items[0].ContainerID != "c-1" || items[0].Labels["shinyhub.deployment_id"] != "7" {
		t.Errorf("item = %+v", items[0])
	}
	if items[0].URL != "https://w:8443/v1/data/tok" {
		t.Errorf("URL = %q, want tunnel URL", items[0].URL)
	}
}

func TestRemoteRuntime_Inventory_ReconcilesAgainstAgent(t *testing.T) {
	dir := t.TempDir()
	rt := &fakeLister{containers: []process.ContainerInfo{
		{ID: "c-1", Labels: map[string]string{"shinyhub.slug": "app", "shinyhub.replica_index": "0"}},
	}}
	srv := NewReplicaServer(ReplicaServerConfig{
		Runtime: rt, DataDir: dir, NodeID: "node-a", Advertise: "w:8443",
		AllocatePort: func() int { return 49001 },
	})
	srv.mu.Lock()
	record := &replicaRecord{token: "tok", containerID: "c-1", hostPort: 49001}
	srv.byContainer["c-1"] = record
	srv.byToken["tok"] = record
	srv.mu.Unlock()

	router := chi.NewRouter()
	srv.Routes(router)
	ts := httptest.NewServer(router)
	defer ts.Close()

	lookup := newStubLookup(db.Worker{NodeID: "node-a", Tier: "remote", AdvertiseAddr: "w:8443", Status: "up"})
	rr := newRemoteRuntime(lookup, "remote", &stubDialer{client: ts.Client(), base: ts.URL})

	items, err := rr.Inventory(context.Background())
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if len(items) != 1 || items[0].ContainerID != "c-1" {
		t.Fatalf("items = %+v", items)
	}
	// Capability is satisfied.
	var _ process.ReplicaInventory = rr
}

// perWorkerInventoryDialer routes each worker to its own inventory base URL,
// returning a single item naming the worker so the aggregation can be checked.
type perWorkerInventoryDialer struct {
	client *http.Client
	bases  map[string]string // nodeID -> base URL
}

func (d *perWorkerInventoryDialer) DialWorker(w db.Worker) (*http.Client, string, error) {
	return d.client, d.bases[w.NodeID], nil
}
func (d *perWorkerInventoryDialer) Transport(db.Worker) (http.RoundTripper, error) {
	return d.client.Transport, nil
}

// TestRemoteRuntime_InventoryAggregatesAllUpWorkers asserts that inventory spans
// every up worker on the tier, not just the routing worker: once distinct-address
// workers coexist, a replica can live on any of them, so recovery must see them
// all or it would miss (and leak) replicas on the non-routing workers.
func TestRemoteRuntime_InventoryAggregatesAllUpWorkers(t *testing.T) {
	mk := func(container string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode([]api.InventoryItem{
				{ContainerID: container, Running: true, URL: "https://x/" + container},
			})
		}))
	}
	sa := mk("ca")
	defer sa.Close()
	sb := mk("cb")
	defer sb.Close()

	lookup := newStubLookup(
		db.Worker{NodeID: "node-a", Tier: "remote", AdvertiseAddr: "a:8443", Status: "up"},
		db.Worker{NodeID: "node-b", Tier: "remote", AdvertiseAddr: "b:8443", Status: "up"},
	)
	dialer := &perWorkerInventoryDialer{
		client: sa.Client(),
		bases:  map[string]string{"node-a": sa.URL, "node-b": sb.URL},
	}
	rr := newRemoteRuntime(lookup, "remote", dialer)

	items, err := rr.Inventory(context.Background())
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	got := map[string]bool{}
	for _, it := range items {
		got[it.ContainerID] = true
	}
	if !got["ca"] || !got["cb"] {
		t.Fatalf("inventory = %+v, want items from both workers (ca, cb)", items)
	}
}

// failingWorkerDialer serves one worker over the given client/base and fails the
// dial for any other worker, modelling a tier where one worker is reachable and
// another is not.
type failingWorkerDialer struct {
	okNode string
	client *http.Client
	base   string
}

func (d *failingWorkerDialer) DialWorker(w db.Worker) (*http.Client, string, error) {
	if w.NodeID != d.okNode {
		return nil, "", fmt.Errorf("dial %s: unreachable", w.NodeID)
	}
	return d.client, d.base, nil
}
func (d *failingWorkerDialer) Transport(db.Worker) (http.RoundTripper, error) {
	return d.client.Transport, nil
}

// TestRemoteRuntime_InventoryPartialFailureReportsUnreachable asserts that when
// one worker on the tier is reachable and another is not, Inventory returns the
// reachable worker's items (each stamped with its owning worker) alongside a
// *process.PartialInventoryError naming the unreachable worker, so recovery can
// tell a genuinely-missing replica from one whose owner could not be queried.
func TestRemoteRuntime_InventoryPartialFailureReportsUnreachable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]api.InventoryItem{
			{ContainerID: "ca", Running: true, URL: "https://x/ca"},
		})
	}))
	defer ts.Close()

	lookup := newStubLookup(
		db.Worker{NodeID: "node-a", Tier: "remote", AdvertiseAddr: "a:8443", Status: "up"},
		db.Worker{NodeID: "node-b", Tier: "remote", AdvertiseAddr: "b:8443", Status: "up"},
	)
	dialer := &failingWorkerDialer{okNode: "node-a", client: ts.Client(), base: ts.URL}
	rr := newRemoteRuntime(lookup, "remote", dialer)

	items, err := rr.Inventory(context.Background())

	var partial *process.PartialInventoryError
	if !errors.As(err, &partial) {
		t.Fatalf("err = %v, want *process.PartialInventoryError", err)
	}
	if len(partial.Workers) != 1 || partial.Workers[0] != "node-b" {
		t.Fatalf("unreachable workers = %v, want [node-b]", partial.Workers)
	}
	if len(items) != 1 || items[0].ContainerID != "ca" {
		t.Fatalf("items = %+v, want only node-a's container", items)
	}
	if items[0].WorkerID != "node-a" {
		t.Fatalf("item WorkerID = %q, want node-a (owning worker stamped)", items[0].WorkerID)
	}
}

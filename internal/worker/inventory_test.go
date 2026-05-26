package worker

import (
	"context"
	"encoding/json"
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

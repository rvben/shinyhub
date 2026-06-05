package lifecycle

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestMatchInventoryItem(t *testing.T) {
	const wantWorker = "node-x"
	items := []process.InventoryItem{
		{ContainerID: "c-1", Running: true, URL: "https://w:8443/v1/data/tok", WorkerID: wantWorker,
			Labels: map[string]string{
				"shinyhub.slug": "app", "shinyhub.replica_index": "0",
				"shinyhub.deployment_id": "7",
			}},
	}

	// Current deployment: matches.
	got := matchInventoryItem(items, "app", 0, "7", wantWorker)
	if got == nil {
		t.Fatal("current-deployment replica not matched")
	}
	if got.URL != "https://w:8443/v1/data/tok" {
		t.Errorf("URL = %q", got.URL)
	}
	// Superseded deployment (same slug+index, different deployment) must NOT match.
	if stale := matchInventoryItem(items, "app", 0, "9", wantWorker); stale != nil {
		t.Errorf("stale-deployment container was matched: %+v", stale)
	}
	// Wrong index does not match.
	if mismatch := matchInventoryItem(items, "app", 1, "7", wantWorker); mismatch != nil {
		t.Errorf("wrong-index match: %+v", mismatch)
	}
	// Empty deploymentID (legacy replica row) matches on slug+index alone.
	if legacy := matchInventoryItem(items, "app", 0, "", wantWorker); legacy == nil {
		t.Error("legacy empty-deployment replica should match on slug+index")
	}
}

// TestMatchInventoryItem_RequiresOwningWorker asserts that with inventory now
// aggregated across multiple workers, a replica row is matched only to a
// container reported by the worker that owns it. Two workers can report
// same-labeled containers (a stale leftover from a failed move, or a duplicate
// placement); matching the row to the wrong worker's item would adopt that
// worker's URL while encoding the handle and selecting the transport for the
// owning worker, breaking the route.
func TestMatchInventoryItem_RequiresOwningWorker(t *testing.T) {
	labels := map[string]string{
		"shinyhub.slug": "app", "shinyhub.replica_index": "0", "shinyhub.deployment_id": "7",
	}
	items := []process.InventoryItem{
		{ContainerID: "c-other", Running: true, URL: "https://a:8443/v1/data/aaa", WorkerID: "node-a", Labels: labels},
		{ContainerID: "c-mine", Running: true, URL: "https://b:8443/v1/data/bbb", WorkerID: "node-b", Labels: labels},
	}

	got := matchInventoryItem(items, "app", 0, "7", "node-b")
	if got == nil {
		t.Fatal("replica owned by node-b not matched against node-b's container")
	}
	if got.ContainerID != "c-mine" || got.URL != "https://b:8443/v1/data/bbb" {
		t.Fatalf("matched the wrong worker's container: %+v", got)
	}

	// A worker that reports no matching container yields no match, even though
	// another worker has a same-labeled container.
	if cross := matchInventoryItem(items, "app", 0, "7", "node-c"); cross != nil {
		t.Fatalf("matched a container from a non-owning worker: %+v", cross)
	}
}

// TestRecoverRemoteReplica_PersistsLiveEndpoint asserts that re-adopting a
// remote replica persists the inventory URL it registered with back to the
// replica row, so the stored endpoint_url matches the live proxy route. The
// worker-loss path deregisters a slot only while the live route still equals
// the row's endpoint_url; if recovery left a stale (or legacy empty) endpoint,
// a later loss/revoke pass would refuse to evict the route and keep sending
// traffic to a dead worker.
func TestRecoverRemoteReplica_PersistsLiveEndpoint(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "u", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, err := store.GetUserByUsername("u")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: "app", Name: "app", OwnerID: u.ID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("app")
	if err != nil {
		t.Fatal(err)
	}
	app.Replicas = 1

	// Seed the replica with a STALE endpoint_url (e.g. from a prior boot or a
	// legacy row), distinct from the URL the live inventory now reports.
	const liveURL = "https://w:8443/v1/data/fresh"
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: db.ReplicaStatusRunning,
		Provider: "remote_docker", Tier: "remote", WorkerID: "node-x",
		EndpointURL: "https://w:8443/v1/data/stale",
	}); err != nil {
		t.Fatal(err)
	}
	r := &db.Replica{AppID: app.ID, Index: 0, Status: db.ReplicaStatusRunning,
		Provider: "remote_docker", Tier: "remote", WorkerID: "node-x",
		EndpointURL: "https://w:8443/v1/data/stale"}

	items := []process.InventoryItem{
		{ContainerID: "c-1", Running: true, URL: liveURL, WorkerID: "node-x",
			Labels: map[string]string{"shinyhub.slug": "app", "shinyhub.replica_index": "0"}},
	}

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	prx := proxy.New()
	prx.SetPoolSize("app", 1)

	if !recoverRemoteReplica(store, mgr, prx, app, r, items) {
		t.Fatal("recoverRemoteReplica returned false for a live replica")
	}

	if got := prx.ReplicaTargetURL("app", 0); got != liveURL {
		t.Fatalf("proxy route = %q, want live inventory URL %q", got, liveURL)
	}
	reps, err := store.ListReplicas(app.ID)
	if err != nil || len(reps) != 1 {
		t.Fatalf("ListReplicas = %+v err=%v", reps, err)
	}
	if reps[0].EndpointURL != liveURL {
		t.Fatalf("stored endpoint_url = %q, want live route %q (loss path would refuse to evict on a mismatch)", reps[0].EndpointURL, liveURL)
	}
}

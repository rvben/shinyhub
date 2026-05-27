package lifecycle

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestMatchInventoryItem(t *testing.T) {
	items := []process.InventoryItem{
		{ContainerID: "c-1", Running: true, URL: "https://w:8443/v1/data/tok",
			Labels: map[string]string{
				"shinyhub.slug": "app", "shinyhub.replica_index": "0",
				"shinyhub.deployment_id": "7",
			}},
	}

	// Current deployment: matches.
	got := matchInventoryItem(items, "app", 0, "7")
	if got == nil {
		t.Fatal("current-deployment replica not matched")
	}
	if got.URL != "https://w:8443/v1/data/tok" {
		t.Errorf("URL = %q", got.URL)
	}
	// Superseded deployment (same slug+index, different deployment) must NOT match.
	if stale := matchInventoryItem(items, "app", 0, "9"); stale != nil {
		t.Errorf("stale-deployment container was matched: %+v", stale)
	}
	// Wrong index does not match.
	if mismatch := matchInventoryItem(items, "app", 1, "7"); mismatch != nil {
		t.Errorf("wrong-index match: %+v", mismatch)
	}
	// Empty deploymentID (legacy replica row) matches on slug+index alone.
	if legacy := matchInventoryItem(items, "app", 0, ""); legacy == nil {
		t.Error("legacy empty-deployment replica should match on slug+index")
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
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
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
		{ContainerID: "c-1", Running: true, URL: liveURL,
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

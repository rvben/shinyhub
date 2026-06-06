// internal/worker/registry_test.go
package worker

import (
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func newTestStore(t *testing.T) *db.Store {
	t.Helper()
	return dbtest.New(t)
}

// joinUp registers a worker and brings it up the normal way: a heartbeat.
// Register alone leaves a worker "joining" (not routable); it becomes routable
// only on its first heartbeat, mirroring how an agent reports in only after its
// data-plane listener has bound. Tests that need a routable worker use this.
func joinUp(t *testing.T, reg *Registry, p RegisterParams) db.Worker {
	t.Helper()
	w, err := reg.Register(p)
	if err != nil {
		t.Fatalf("register %s: %v", p.AdvertiseAddr, err)
	}
	if err := reg.Heartbeat(w.NodeID, p.Fingerprint); err != nil {
		t.Fatalf("heartbeat %s: %v", w.NodeID, err)
	}
	up, ok := reg.Worker(w.NodeID)
	if !ok {
		t.Fatalf("worker %s missing after join", w.NodeID)
	}
	return up
}

// TestRegistry_RefreshRebuildsFromStore simulates failover: an "active" instance
// (regA) registers a worker that the "standby" (regB) has not seen, then regB
// refreshes on takeover and routes to it. Refresh is a full replace, so a worker
// removed from the store also disappears from the index.
func TestRegistry_RefreshRebuildsFromStore(t *testing.T) {
	store := newTestStore(t)
	regA, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry A: %v", err)
	}
	regB, err := NewRegistry(store) // standby, built before A registers anything
	if err != nil {
		t.Fatalf("new registry B: %v", err)
	}

	w := joinUp(t, regA, RegisterParams{AdvertiseAddr: "203.0.113.5:9000", Tier: "burst", Fingerprint: "fp"})

	// The standby's index is the boot snapshot and does not see A's new worker.
	if _, ok := regB.Worker(w.NodeID); ok {
		t.Fatal("standby saw the worker before refresh (precondition: stale by design)")
	}

	// On takeover, the standby refreshes from the shared store and routes to it.
	if err := regB.Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, ok := regB.Worker(w.NodeID); !ok {
		t.Fatal("refresh did not pick up the worker the other instance wrote")
	}
	if _, ok := regB.WorkerForTier("burst"); !ok {
		t.Fatal("refreshed worker is not routable on its tier")
	}

	// Full replace: a worker removed from the store disappears on the next refresh.
	if err := store.DeleteWorker(w.NodeID); err != nil {
		t.Fatalf("delete worker: %v", err)
	}
	if err := regB.Refresh(); err != nil {
		t.Fatalf("refresh after delete: %v", err)
	}
	if _, ok := regB.Worker(w.NodeID); ok {
		t.Fatal("refresh did not drop a worker removed from the store")
	}
}

// TestRegistry_RefreshSurfacesStoreError asserts Refresh returns the underlying
// store error (so the owner-acquire path keeps the readiness gate closed rather
// than routing on a stale index).
func TestRegistry_RefreshSurfacesStoreError(t *testing.T) {
	store := newTestStore(t)
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	_ = store.Close() // ListWorkers now fails
	if err := reg.Refresh(); err == nil {
		t.Fatal("Refresh did not surface the store error after Close")
	}
}

// TestRegistryRegisterIsJoiningUntilHeartbeat asserts a freshly registered worker
// is "joining" and NOT routable: it becomes routable only once it heartbeats.
// This closes the deploy race where the control plane dialed a worker that had
// registered but not yet bound its data-plane listener (connection refused). A
// worker reports in (heartbeats) only after Listen() binds, so gating routing on
// the heartbeat guarantees an "up" worker is actually listening.
func TestRegistryRegisterIsJoiningUntilHeartbeat(t *testing.T) {
	store := newTestStore(t)
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	node, err := reg.Register(RegisterParams{AdvertiseAddr: "10.0.0.5:8443", Tier: "burst", Fingerprint: "aa"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	if w, ok := reg.Worker(node.NodeID); !ok || w.Status != "joining" {
		t.Fatalf("after register, status = %q ok=%v, want joining", w.Status, ok)
	}
	if sw, _ := store.GetWorker(node.NodeID); sw == nil || sw.Status != "joining" {
		t.Fatalf("after register, store status = %+v, want joining", sw)
	}
	if _, ok := reg.WorkerForTier("burst"); ok {
		t.Fatal("a joining worker must not be routable")
	}
	if got := reg.PlanPlacementForTier("burst", "app", 1); len(got) != 0 {
		t.Fatalf("a joining worker must not be a placement candidate, got %+v", got)
	}
}

// TestRegistryHeartbeatPromotesJoiningToUp asserts the first heartbeat from a
// joining worker promotes it to up and routable.
func TestRegistryHeartbeatPromotesJoiningToUp(t *testing.T) {
	store := newTestStore(t)
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	node, err := reg.Register(RegisterParams{AdvertiseAddr: "10.0.0.5:8443", Tier: "burst", Fingerprint: "aa"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.Heartbeat(node.NodeID, "aa"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	if w, ok := reg.Worker(node.NodeID); !ok || w.Status != "up" {
		t.Fatalf("after heartbeat, status = %q ok=%v, want up", w.Status, ok)
	}
	got, ok := reg.WorkerForTier("burst")
	if !ok || got.NodeID != node.NodeID {
		t.Fatalf("WorkerForTier(burst) = %+v ok=%v, want %s routable after heartbeat", got, ok, node.NodeID)
	}
}

func TestRegistryRegisterAndLookup(t *testing.T) {
	reg, err := NewRegistry(newTestStore(t))
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	node := joinUp(t, reg, RegisterParams{
		Name:          "burst-a",
		AdvertiseAddr: "10.0.0.5:8443",
		Tier:          "burst",
		Version:       "v0.6.0",
		Fingerprint:   "ab12",
	})
	if node.NodeID == "" {
		t.Fatal("empty node id allocated")
	}

	got, ok := reg.WorkerForTier("burst")
	if !ok || got.NodeID != node.NodeID {
		t.Fatalf("WorkerForTier(burst) = %+v ok=%v", got, ok)
	}
	if _, ok := reg.WorkerForTier("nonexistent"); ok {
		t.Fatal("WorkerForTier returned a worker for an empty tier")
	}

	byID, ok := reg.Worker(node.NodeID)
	if !ok || byID.AdvertiseAddr != "10.0.0.5:8443" {
		t.Fatalf("Worker(%q) = %+v ok=%v", node.NodeID, byID, ok)
	}
}

// TestRegistryDistinctAddressWorkersCoexistOnTier asserts that two workers
// joining a tier at distinct advertise addresses both stay up: they are real
// multi-worker capacity, not duplicates to be superseded. WorkersForTier returns
// both, sorted by node id, and WorkerForTier deterministically returns the first
// of that set (lowest node id) rather than depending on map iteration order.
func TestRegistryDistinctAddressWorkersCoexistOnTier(t *testing.T) {
	store := newTestStore(t)
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	first := joinUp(t, reg, RegisterParams{AdvertiseAddr: "10.0.0.5:8443", Tier: "burst", Fingerprint: "aa"})
	second := joinUp(t, reg, RegisterParams{AdvertiseAddr: "10.0.0.6:8443", Tier: "burst", Fingerprint: "bb"})

	// Both workers stay up in the store: distinct addresses are not superseded.
	for _, id := range []string{first.NodeID, second.NodeID} {
		if w, _ := store.GetWorker(id); w == nil || w.Status != "up" {
			t.Fatalf("worker %s status = %+v, want up in store", id, w)
		}
	}

	all := reg.WorkersForTier("burst")
	if len(all) != 2 {
		t.Fatalf("WorkersForTier(burst) returned %d workers, want 2: %+v", len(all), all)
	}
	if all[0].NodeID >= all[1].NodeID {
		t.Fatalf("WorkersForTier not sorted by node id: %s, %s", all[0].NodeID, all[1].NodeID)
	}

	// WorkerForTier is deterministic: always the first of WorkersForTier.
	for i := 0; i < 50; i++ {
		got, ok := reg.WorkerForTier("burst")
		if !ok || got.NodeID != all[0].NodeID {
			t.Fatalf("WorkerForTier(burst) = %+v ok=%v, want %s", got, ok, all[0].NodeID)
		}
	}
}

// TestRegistrySameAddressReregisterSupersedesStaleNode asserts that a worker
// rejoining at an advertise address already owned by an up worker (an agent that
// lost its persisted identity and registered under a fresh node id) supersedes
// the stale node at that endpoint: the old node is marked down and only the new
// one routes for that address.
func TestRegistrySameAddressReregisterSupersedesStaleNode(t *testing.T) {
	store := newTestStore(t)
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	stale := joinUp(t, reg, RegisterParams{AdvertiseAddr: "10.0.0.5:8443", Tier: "burst", Fingerprint: "aa"})
	fresh, err := reg.Register(RegisterParams{AdvertiseAddr: "10.0.0.5:8443", Tier: "burst", Fingerprint: "bb"})
	if err != nil {
		t.Fatalf("register fresh: %v", err)
	}

	// Registering fresh at an endpoint the stale node holds supersedes it at once:
	// the stale node id no longer matches the cert the rejoined listener presents,
	// so routing must drop it immediately rather than dial a stale identity. fresh
	// stays "joining" (not routable) until its first heartbeat.
	if w, _ := store.GetWorker(stale.NodeID); w == nil || w.Status != "down" {
		t.Fatalf("stale worker %s status = %+v, want down in store", stale.NodeID, w)
	}
	if got, ok := reg.Worker(stale.NodeID); !ok || got.Status != "down" {
		t.Fatalf("stale worker %s in-memory status = %+v ok=%v, want down", stale.NodeID, got, ok)
	}
	if _, ok := reg.WorkerForTier("burst"); ok {
		t.Fatal("endpoint must have no routable worker between superseding the stale node and the replacement's first heartbeat")
	}

	// fresh becomes routable on its first heartbeat (promoted joining->up; the
	// stale node is already down so it does not block the promotion).
	if err := reg.Heartbeat(fresh.NodeID, "bb"); err != nil {
		t.Fatalf("heartbeat fresh: %v", err)
	}
	all := reg.WorkersForTier("burst")
	if len(all) != 1 || all[0].NodeID != fresh.NodeID {
		t.Fatalf("WorkersForTier(burst) = %+v, want only %s", all, fresh.NodeID)
	}
}

// TestRegistryWorkersForTierReturnsAllUp asserts WorkersForTier reflects liveness:
// it returns every up worker on the tier (sorted), and a worker marked down drops
// out of the set.
func TestRegistryWorkersForTierReturnsAllUp(t *testing.T) {
	reg, err := NewRegistry(newTestStore(t))
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	a := joinUp(t, reg, RegisterParams{AdvertiseAddr: "10.0.0.5:8443", Tier: "burst", Fingerprint: "aa"})
	b := joinUp(t, reg, RegisterParams{AdvertiseAddr: "10.0.0.6:8443", Tier: "burst", Fingerprint: "bb"})
	if got := reg.WorkersForTier("burst"); len(got) != 2 {
		t.Fatalf("WorkersForTier(burst) = %d workers, want 2", len(got))
	}
	if got := reg.WorkersForTier("other"); len(got) != 0 {
		t.Fatalf("WorkersForTier(other) = %d workers, want 0", len(got))
	}

	down := a.NodeID
	if err := reg.MarkDown(down); err != nil {
		t.Fatalf("mark down: %v", err)
	}
	got := reg.WorkersForTier("burst")
	if len(got) != 1 || got[0].NodeID != b.NodeID {
		t.Fatalf("WorkersForTier(burst) after marking %s down = %+v, want only %s", down, got, b.NodeID)
	}
}

// TestRegistryConcurrentSameTierRegistrationsConverge asserts that many workers
// at one (tier, advertise address) converge to a single up worker the store and
// the in-memory index agree on. Registration now yields "joining" (not routable);
// the endpoint-ownership decision is made when those joining workers first
// heartbeat. Heartbeat serializes that decision, so concurrent first heartbeats
// cannot each mark the others down and leave zero up, nor promote two at once and
// leave the endpoint with two routing candidates.
func TestRegistryConcurrentSameTierRegistrationsConverge(t *testing.T) {
	store := newTestStore(t)
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	const n = 16
	nodes := make([]string, n)
	for i := 0; i < n; i++ {
		w, err := reg.Register(RegisterParams{AdvertiseAddr: "10.0.0.5:8443", Tier: "burst", Fingerprint: "aa"})
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		nodes[i] = w.NodeID
	}
	// Freshly registered workers are joining; none routes before its heartbeat.
	if _, ok := reg.WorkerForTier("burst"); ok {
		t.Fatal("a joining worker must not be routable before its heartbeat")
	}

	var wg sync.WaitGroup
	wg.Add(n)
	for _, id := range nodes {
		go func(id string) {
			defer wg.Done()
			if err := reg.Heartbeat(id, "aa"); err != nil {
				t.Errorf("heartbeat: %v", err)
			}
		}(id)
	}
	wg.Wait()

	// Exactly one worker is up in the store, matching the in-memory routing slot.
	all, err := store.ListWorkers()
	if err != nil {
		t.Fatalf("list workers: %v", err)
	}
	var upInStore []string
	for _, w := range all {
		if w.Status == "up" {
			upInStore = append(upInStore, w.NodeID)
		}
	}
	if len(upInStore) != 1 {
		t.Fatalf("up workers in store = %v, want exactly 1", upInStore)
	}
	routed, ok := reg.WorkerForTier("burst")
	if !ok {
		t.Fatal("WorkerForTier(burst) found no worker after concurrent heartbeats")
	}
	if routed.NodeID != upInStore[0] {
		t.Fatalf("routing slot %s disagrees with the up worker in store %s", routed.NodeID, upInStore[0])
	}
}

// TestRegistryHeartbeatDoesNotResurrectSupersededWorker asserts that a heartbeat
// from a worker superseded at its endpoint (it lost its identity and rejoined
// under a fresh node id at the same advertise address) does not flip it back to
// up alongside the newer owner of that (tier, address). The fresh registrant
// keeps the address's routing slot; the heartbeat still refreshes the superseded
// worker's fingerprint and liveness.
func TestRegistryHeartbeatDoesNotResurrectSupersededWorker(t *testing.T) {
	store := newTestStore(t)
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	stale := joinUp(t, reg, RegisterParams{AdvertiseAddr: "10.0.0.5:8443", Tier: "burst", Fingerprint: "aa"})
	fresh, err := reg.Register(RegisterParams{AdvertiseAddr: "10.0.0.5:8443", Tier: "burst", Fingerprint: "bb"})
	if err != nil {
		t.Fatalf("register fresh: %v", err)
	}
	// fresh registering supersedes the up stale node, then heartbeats up to own
	// the endpoint's routing slot.
	if err := reg.Heartbeat(fresh.NodeID, "bb"); err != nil {
		t.Fatalf("heartbeat fresh: %v", err)
	}

	// The superseded worker heartbeats under its old node id.
	if err := reg.Heartbeat(stale.NodeID, "aa2"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	got, ok := reg.WorkerForTier("burst")
	if !ok || got.NodeID != fresh.NodeID {
		t.Fatalf("WorkerForTier(burst) = %+v ok=%v, want %s", got, ok, fresh.NodeID)
	}
	w, ok := reg.Worker(stale.NodeID)
	if !ok || w.Status != "down" {
		t.Errorf("superseded worker re-upped via heartbeat: %+v ok=%v", w, ok)
	}
	if w.Fingerprint != "aa2" {
		t.Errorf("heartbeat did not refresh superseded worker fingerprint: %+v", w)
	}
	if sw, _ := store.GetWorker(stale.NodeID); sw == nil || sw.Status != "down" {
		t.Errorf("superseded worker re-upped in store: %+v", sw)
	}
}

// TestRegistryHeartbeatReupsWhenTierSlotFree asserts that a worker reaped for
// missed heartbeats (marked down with no successor) recovers its routing slot on
// its next heartbeat, since the tier is otherwise unowned.
func TestRegistryHeartbeatReupsWhenTierSlotFree(t *testing.T) {
	store := newTestStore(t)
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	node, err := reg.Register(RegisterParams{AdvertiseAddr: "10.0.0.5:8443", Tier: "burst", Fingerprint: "aa"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.MarkDown(node.NodeID); err != nil {
		t.Fatalf("mark down: %v", err)
	}
	if _, ok := reg.WorkerForTier("burst"); ok {
		t.Fatal("tier slot should be free after the only worker was marked down")
	}

	if err := reg.Heartbeat(node.NodeID, "bb"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	got, ok := reg.WorkerForTier("burst")
	if !ok || got.NodeID != node.NodeID {
		t.Fatalf("recovered worker not re-upped: %+v ok=%v", got, ok)
	}
}

func TestRegistryMarkDownExcludesFromRouting(t *testing.T) {
	store := newTestStore(t)
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	node, err := reg.Register(RegisterParams{AdvertiseAddr: "10.0.0.5:8443", Tier: "burst", Fingerprint: "aa"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.MarkDown(node.NodeID); err != nil {
		t.Fatalf("mark down: %v", err)
	}
	if _, ok := reg.WorkerForTier("burst"); ok {
		t.Fatal("WorkerForTier returned a worker after it was marked down")
	}
	if w, _ := store.GetWorker(node.NodeID); w == nil || w.Status != "down" {
		t.Fatalf("worker status = %+v, want down in store", w)
	}
}

func TestRegistryRebuildsFromStore(t *testing.T) {
	store := newTestStore(t)
	if err := store.UpsertWorker(db.Worker{
		NodeID: "node-x", AdvertiseAddr: "1.2.3.4:9", Tier: "burst", Status: "up",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if _, ok := reg.WorkerForTier("burst"); !ok {
		t.Fatal("registry did not rebuild in-memory index from store on construction")
	}
}

// seedRunningReplica creates app slug (once) and a running replica of it on
// workerID, for placement-load tests.
func seedRunningReplica(t *testing.T, store *db.Store, ownerID int64, slug string, idx int, workerID string) {
	t.Helper()
	if _, err := store.GetAppBySlug(slug); err != nil {
		if err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: ownerID, Access: "private"}); err != nil {
			t.Fatalf("create app %q: %v", slug, err)
		}
	}
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatalf("get app %q: %v", slug, err)
	}
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: idx, Status: db.ReplicaStatusRunning,
		Provider: "remote_docker", Tier: "remote", WorkerID: workerID,
	}); err != nil {
		t.Fatalf("seed replica %s#%d on %s: %v", slug, idx, workerID, err)
	}
}

func mustSeedOwner(t *testing.T, store *db.Store) int64 {
	t.Helper()
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	u, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	return u.ID
}

// TestRegistryPlanPlacementForTier_LeastLoaded asserts that placement picks the
// up worker hosting the fewest running replicas, so multi-worker capacity is
// actually used instead of stacking every replica on one worker.
func TestRegistryPlanPlacementForTier_LeastLoaded(t *testing.T) {
	store := newTestStore(t)
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	a := joinUp(t, reg, RegisterParams{AdvertiseAddr: "a:8443", Tier: "remote", Fingerprint: "aa"})
	b := joinUp(t, reg, RegisterParams{AdvertiseAddr: "b:8443", Tier: "remote", Fingerprint: "bb"})
	owner := mustSeedOwner(t, store)

	// node-a already hosts two replicas; node-b hosts none.
	seedRunningReplica(t, store, owner, "app", 0, a.NodeID)
	seedRunningReplica(t, store, owner, "app", 1, a.NodeID)

	got := reg.PlanPlacementForTier("remote", "app", 1)
	if len(got) != 1 {
		t.Fatalf("PlanPlacementForTier returned %d workers, want 1", len(got))
	}
	if got[0].NodeID != b.NodeID {
		t.Fatalf("placed on %s (load), want least-loaded %s", got[0].NodeID, b.NodeID)
	}
}

// TestRegistryPlanPlacementForTier_AntiAffinityTiebreak asserts that on equal
// total load, placement prefers the worker not already hosting another replica
// of the same app, spreading an app's replicas across workers for HA.
func TestRegistryPlanPlacementForTier_AntiAffinityTiebreak(t *testing.T) {
	store := newTestStore(t)
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	a := joinUp(t, reg, RegisterParams{AdvertiseAddr: "a:8443", Tier: "remote", Fingerprint: "aa"})
	b := joinUp(t, reg, RegisterParams{AdvertiseAddr: "b:8443", Tier: "remote", Fingerprint: "bb"})
	owner := mustSeedOwner(t, store)

	// Equal total load (one each), but node-a already hosts the candidate app.
	seedRunningReplica(t, store, owner, "app", 0, a.NodeID)
	seedRunningReplica(t, store, owner, "other", 0, b.NodeID)

	got := reg.PlanPlacementForTier("remote", "app", 1)
	if len(got) != 1 {
		t.Fatalf("PlanPlacementForTier returned %d workers, want 1", len(got))
	}
	if got[0].NodeID != b.NodeID {
		t.Fatalf("placed on %s, want %s (anti-affinity: avoid co-locating app's own replicas)", got[0].NodeID, b.NodeID)
	}
}

// TestRegistryPlanPlacementForTier_DeterministicOnFullTie asserts that with no
// load to differentiate workers, single placement is deterministic (lowest node
// id), matching WorkersForTier ordering, and that an empty tier yields nothing.
func TestRegistryPlanPlacementForTier_DeterministicOnFullTie(t *testing.T) {
	store := newTestStore(t)
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	joinUp(t, reg, RegisterParams{AdvertiseAddr: "a:8443", Tier: "remote", Fingerprint: "aa"})
	joinUp(t, reg, RegisterParams{AdvertiseAddr: "b:8443", Tier: "remote", Fingerprint: "bb"})
	want := reg.WorkersForTier("remote")[0].NodeID

	for i := 0; i < 25; i++ {
		got := reg.PlanPlacementForTier("remote", "app", 1)
		if len(got) != 1 || got[0].NodeID != want {
			t.Fatalf("PlanPlacementForTier = %+v, want deterministic [%s]", got, want)
		}
	}

	if got := reg.PlanPlacementForTier("nonexistent", "app", 1); len(got) != 0 {
		t.Fatalf("PlanPlacementForTier on empty tier = %+v, want nil", got)
	}
}

// TestRegistryPlanPlacementForTier_SpreadsBatchAcrossWorkers is the multi-replica
// case the concurrent deploy path needs: planning two replicas for one app on an
// empty two-worker tier must put one on each worker. A per-replica placement that
// re-reads the same pre-deploy load snapshot would pick the lowest node id for
// both and co-locate them; folding each assignment into a running tally spreads
// them.
func TestRegistryPlanPlacementForTier_SpreadsBatchAcrossWorkers(t *testing.T) {
	store := newTestStore(t)
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	joinUp(t, reg, RegisterParams{AdvertiseAddr: "a:8443", Tier: "remote", Fingerprint: "aa"})
	joinUp(t, reg, RegisterParams{AdvertiseAddr: "b:8443", Tier: "remote", Fingerprint: "bb"})

	got := reg.PlanPlacementForTier("remote", "app", 2)
	if len(got) != 2 {
		t.Fatalf("PlanPlacementForTier(count=2) returned %d workers, want 2", len(got))
	}
	if got[0].NodeID == got[1].NodeID {
		t.Fatalf("both replicas planned onto %s; want one per worker across the tier", got[0].NodeID)
	}
}

// TestRegistryPlanPlacementForTier_BalancesUnevenBatch asserts a batch larger
// than the worker count distributes as evenly as the greedy least-loaded policy
// allows: three replicas across two empty workers split 2-1, never 3-0.
func TestRegistryPlanPlacementForTier_BalancesUnevenBatch(t *testing.T) {
	store := newTestStore(t)
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	joinUp(t, reg, RegisterParams{AdvertiseAddr: "a:8443", Tier: "remote", Fingerprint: "aa"})
	joinUp(t, reg, RegisterParams{AdvertiseAddr: "b:8443", Tier: "remote", Fingerprint: "bb"})

	got := reg.PlanPlacementForTier("remote", "app", 3)
	if len(got) != 3 {
		t.Fatalf("PlanPlacementForTier(count=3) returned %d workers, want 3", len(got))
	}
	counts := map[string]int{}
	for _, w := range got {
		counts[w.NodeID]++
	}
	if len(counts) != 2 {
		t.Fatalf("batch spread across %d workers, want 2: %v", len(counts), counts)
	}
	for node, n := range counts {
		if n < 1 || n > 2 {
			t.Fatalf("worker %s got %d replicas, want a 2-1 split", node, n)
		}
	}
}

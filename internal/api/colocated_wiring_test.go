package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/worker"
)

func TestCheckColocatedSharedWiring(t *testing.T) {
	store := dbtest.New(t)

	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir()},
		Runtime: config.RuntimeConfig{Tiers: []config.TierConfig{
			{Name: "local", Runtime: "native"},
			{Name: "burst", Runtime: "docker"},
		}},
	}
	srv := New(cfg, store, nil, nil)

	// Create an owner user.
	if err := store.CreateUser(db.CreateUserParams{
		Username:     "owner",
		PasswordHash: "hash",
		Role:         "admin",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatalf("get owner: %v", err)
	}

	// Create source app.
	if err := store.CreateApp(db.CreateAppParams{
		Slug:    "source",
		Name:    "Source",
		OwnerID: owner.ID,
		Access:  "private",
	}); err != nil {
		t.Fatalf("create source app: %v", err)
	}
	source, err := store.GetAppBySlug("source")
	if err != nil {
		t.Fatalf("get source: %v", err)
	}

	// Create consumer app.
	if err := store.CreateApp(db.CreateAppParams{
		Slug:    "consumer",
		Name:    "Consumer",
		OwnerID: owner.ID,
		Access:  "private",
	}); err != nil {
		t.Fatalf("create consumer app: %v", err)
	}
	consumer, err := store.GetAppBySlug("consumer")
	if err != nil {
		t.Fatalf("get consumer: %v", err)
	}

	// Place source on local (control-plane) and consumer on burst (remote).
	sourcePlacement, _ := json.Marshal(map[string]int{"local": 1})
	if err := store.SetAppPlacement(source.ID, string(sourcePlacement), 1); err != nil {
		t.Fatalf("set source placement: %v", err)
	}
	consumerPlacement, _ := json.Marshal(map[string]int{"burst": 1})
	if err := store.SetAppPlacement(consumer.ID, string(consumerPlacement), 1); err != nil {
		t.Fatalf("set consumer placement: %v", err)
	}

	// Grant shared data: consumer mounts source.
	if err := store.GrantSharedData(consumer.ID, source.ID); err != nil {
		t.Fatalf("grant shared data: %v", err)
	}

	// Inject resolver: burst -> remote node, local -> control-plane "".
	srv.SetNodeForTier(func(tier string) string {
		if tier == "burst" {
			return "node-a"
		}
		return ""
	})

	// Cross-node case: consumer on burst (node-a), source on local ("") -> error.
	consumer, err = store.GetAppBySlug("consumer")
	if err != nil {
		t.Fatalf("refresh consumer: %v", err)
	}
	checkErr := srv.checkColocatedShared(consumer.ID, srv.tiersForApp(consumer))
	if checkErr == nil {
		t.Fatal("cross-node: want error, got nil")
	}
	if !strings.Contains(checkErr.Error(), "source") {
		t.Errorf("error should mention source slug; got: %v", checkErr)
	}

	// Co-located case: move source to burst as well.
	sourcePlacement2, _ := json.Marshal(map[string]int{"burst": 1})
	if err := store.SetAppPlacement(source.ID, string(sourcePlacement2), 1); err != nil {
		t.Fatalf("re-set source placement: %v", err)
	}
	source, err = store.GetAppBySlug("source")
	if err != nil {
		t.Fatalf("refresh source: %v", err)
	}
	consumer, err = store.GetAppBySlug("consumer")
	if err != nil {
		t.Fatalf("refresh consumer (co-located): %v", err)
	}
	_ = source
	if err := srv.checkColocatedShared(consumer.ID, srv.tiersForApp(consumer)); err != nil {
		t.Errorf("co-located: want nil, got %v", err)
	}

	// Nil-resolver case: a server without SetNodeForTier returns nil even for
	// cross-node data (single-node no-op).
	srv2 := New(cfg, store, nil, nil)
	// Reset source back to local (cross-node config) for this check.
	sourcePlacementLocal, _ := json.Marshal(map[string]int{"local": 1})
	if err := store.SetAppPlacement(source.ID, string(sourcePlacementLocal), 1); err != nil {
		t.Fatalf("re-set source placement to local: %v", err)
	}
	consumer, err = store.GetAppBySlug("consumer")
	if err != nil {
		t.Fatalf("refresh consumer (nil resolver): %v", err)
	}
	if err := srv2.checkColocatedShared(consumer.ID, srv2.tiersForApp(consumer)); err != nil {
		t.Errorf("nil resolver: want nil, got %v", err)
	}
}

// multiWorkerColocationFixture builds a server with a "burst" tier backed by two
// distinct-address up workers and a consumer that mounts shared data from a
// source, both placed on burst. It returns the server, the source/consumer apps,
// and the two workers' node ids (sorted, as WorkersForTier reports them).
func multiWorkerColocationFixture(t *testing.T) (*Server, *db.App, *db.App, string, string) {
	t.Helper()
	store := dbtest.New(t)

	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir()},
		Runtime: config.RuntimeConfig{Tiers: []config.TierConfig{
			{Name: "local", Runtime: "native"},
			{Name: "burst", Runtime: "docker"},
		}},
	}
	srv := New(cfg, store, nil, nil)

	if err := store.CreateUser(db.CreateUserParams{
		Username: "owner", PasswordHash: "hash", Role: "admin",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatalf("get owner: %v", err)
	}

	for _, slug := range []string{"source", "consumer"} {
		if err := store.CreateApp(db.CreateAppParams{
			Slug: slug, Name: slug, OwnerID: owner.ID, Access: "private",
		}); err != nil {
			t.Fatalf("create app %q: %v", slug, err)
		}
	}
	source, _ := store.GetAppBySlug("source")
	consumer, _ := store.GetAppBySlug("consumer")

	burst, _ := json.Marshal(map[string]int{"burst": 1})
	if err := store.SetAppPlacement(source.ID, string(burst), 1); err != nil {
		t.Fatalf("set source placement: %v", err)
	}
	if err := store.SetAppPlacement(consumer.ID, string(burst), 1); err != nil {
		t.Fatalf("set consumer placement: %v", err)
	}
	if err := store.GrantSharedData(consumer.ID, source.ID); err != nil {
		t.Fatalf("grant shared data: %v", err)
	}

	// Two distinct-address up workers on burst: real multi-worker capacity.
	reg, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	for _, addr := range []string{"10.0.0.1:8443", "10.0.0.2:8443"} {
		w, err := reg.Register(worker.RegisterParams{
			Name: addr, AdvertiseAddr: addr, Tier: "burst", Fingerprint: addr,
		})
		if err != nil {
			t.Fatalf("register worker %q: %v", addr, err)
		}
		// A worker is routable only after its first heartbeat (Register -> joining).
		if _, _, err := reg.Heartbeat(w.NodeID, addr, 0); err != nil {
			t.Fatalf("heartbeat worker %q: %v", addr, err)
		}
	}
	srv.SetWorkerRegistry(reg)
	srv.SetNodeForTier(func(tier string) string {
		if w, ok := reg.WorkerForTier(tier); ok {
			return w.NodeID
		}
		return ""
	})

	ws := reg.WorkersForTier("burst")
	if len(ws) != 2 {
		t.Fatalf("WorkersForTier(burst) = %d workers, want 2", len(ws))
	}
	source, _ = store.GetAppBySlug("source")
	consumer, _ = store.GetAppBySlug("consumer")
	return srv, source, consumer, ws[0].NodeID, ws[1].NodeID
}

// seedRunningReplica makes slug appear to host a running replica on the named
// worker, which is what marks that worker as holding the app's provisioned data
// for colocation purposes.
func seedRunningReplica(t *testing.T, srv *Server, appID int64, idx int, nodeID string) {
	t.Helper()
	if err := srv.store.UpsertReplica(db.UpsertReplicaParams{
		AppID: appID, Index: idx, Status: db.ReplicaStatusRunning,
		Provider: "remote_docker", Tier: "burst", WorkerID: nodeID,
	}); err != nil {
		t.Fatalf("seed running replica: %v", err)
	}
}

// TestResolveColocation_PinsConsumerToSourceWorker asserts that on a multi-worker
// tier a shared-mount consumer is pinned to the worker that actually hosts its
// source's running replica, instead of being rejected. The deterministic
// tier->node map cannot pick the right worker once a tier has several, so the
// control plane resolves the pin from where the source's data really lives.
func TestResolveColocation_PinsConsumerToSourceWorker(t *testing.T) {
	srv, source, consumer, nodeA, _ := multiWorkerColocationFixture(t)

	// Source's data lives on nodeA: it has a running replica there.
	seedRunningReplica(t, srv, source.ID, 0, nodeA)

	pins, err := srv.resolveColocation(consumer.ID, srv.tiersForApp(consumer))
	if err != nil {
		t.Fatalf("resolveColocation: want nil error, got %v", err)
	}
	if len(pins) != 1 || pins[0] != nodeA {
		t.Errorf("pins = %v, want [%s] (the worker hosting the source)", pins, nodeA)
	}

	// The user-facing precheck must accept this deploy.
	if err := srv.checkColocatedShared(consumer.ID, srv.tiersForApp(consumer)); err != nil {
		t.Errorf("checkColocatedShared: want nil, got %v", err)
	}
}

// TestResolveColocation_RejectsWhenNoCommonWorker asserts the colocation check
// fails closed on a multi-worker tier when no worker hosts the source's data
// (the source is not running anywhere), since no pin can place the consumer
// beside its mounted data.
func TestResolveColocation_RejectsWhenNoCommonWorker(t *testing.T) {
	srv, _, consumer, _, _ := multiWorkerColocationFixture(t)

	// Source has no running replica on any worker.
	pins, err := srv.resolveColocation(consumer.ID, srv.tiersForApp(consumer))
	if err == nil {
		t.Fatalf("resolveColocation: want error, got pins=%v nil", pins)
	}
	if pins != nil {
		t.Errorf("pins = %v, want nil on rejection", pins)
	}
	if err := srv.checkColocatedShared(consumer.ID, srv.tiersForApp(consumer)); err == nil {
		t.Error("checkColocatedShared: want error, got nil")
	}
}

// TestResolveColocation_RejectsMultipleWorkerTiers asserts a shared-mount
// consumer placed across more than one worker tier is rejected. The pin is a
// flat worker set applied round-robin to every tier's replicas, so a worker
// belonging to one tier would be stamped onto another tier's replica and the
// remote runtime would reject it as a wrong-tier target. Failing the precheck
// gives a clear 409 instead of a confusing partial deploy.
func TestResolveColocation_RejectsMultipleWorkerTiers(t *testing.T) {
	store := dbtest.New(t)

	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir()},
		Runtime: config.RuntimeConfig{Tiers: []config.TierConfig{
			{Name: "burst", Runtime: "docker"},
			{Name: "burst2", Runtime: "docker"},
		}},
	}
	srv := New(cfg, store, nil, nil)

	if err := store.CreateUser(db.CreateUserParams{
		Username: "owner", PasswordHash: "hash", Role: "admin",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	owner, _ := store.GetUserByUsername("owner")
	for _, slug := range []string{"source", "consumer"} {
		if err := store.CreateApp(db.CreateAppParams{
			Slug: slug, Name: slug, OwnerID: owner.ID, Access: "private",
		}); err != nil {
			t.Fatalf("create app %q: %v", slug, err)
		}
	}
	source, _ := store.GetAppBySlug("source")
	consumer, _ := store.GetAppBySlug("consumer")

	// Source on burst; consumer spans burst and burst2.
	srcPl, _ := json.Marshal(map[string]int{"burst": 1})
	if err := store.SetAppPlacement(source.ID, string(srcPl), 1); err != nil {
		t.Fatalf("set source placement: %v", err)
	}
	conPl, _ := json.Marshal(map[string]int{"burst": 1, "burst2": 1})
	if err := store.SetAppPlacement(consumer.ID, string(conPl), 2); err != nil {
		t.Fatalf("set consumer placement: %v", err)
	}
	if err := store.GrantSharedData(consumer.ID, source.ID); err != nil {
		t.Fatalf("grant shared data: %v", err)
	}

	reg, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	// burst has two workers (triggers the multi-worker pin path); burst2 has one.
	regs := []struct{ addr, tier string }{
		{"10.0.0.1:8443", "burst"},
		{"10.0.0.2:8443", "burst"},
		{"10.0.1.1:8443", "burst2"},
	}
	for _, rp := range regs {
		w, err := reg.Register(worker.RegisterParams{
			Name: rp.addr, AdvertiseAddr: rp.addr, Tier: rp.tier, Fingerprint: rp.addr,
		})
		if err != nil {
			t.Fatalf("register worker %q: %v", rp.addr, err)
		}
		// A worker is routable only after its first heartbeat (Register -> joining).
		if _, _, err := reg.Heartbeat(w.NodeID, rp.addr, 0); err != nil {
			t.Fatalf("heartbeat worker %q: %v", rp.addr, err)
		}
	}
	srv.SetWorkerRegistry(reg)
	srv.SetNodeForTier(func(tier string) string {
		if w, ok := reg.WorkerForTier(tier); ok {
			return w.NodeID
		}
		return ""
	})

	// Source has a running replica on a burst worker, so colocation would
	// otherwise be feasible for the burst replica alone.
	seedRunningReplica(t, srv, source.ID, 0, reg.WorkersForTier("burst")[0].NodeID)

	consumer, _ = store.GetAppBySlug("consumer")
	pins, err := srv.resolveColocation(consumer.ID, srv.tiersForApp(consumer))
	if err == nil {
		t.Fatalf("resolveColocation: want error for multi-tier consumer, got pins=%v nil", pins)
	}
	if pins != nil {
		t.Errorf("pins = %v, want nil on rejection", pins)
	}
}

// TestResolveColocation_ControlPlaneConsumerWithMultiWorkerSource asserts that a
// consumer placed only on the control-plane tier is accepted when its source has
// a control-plane replica, even though the source ALSO spreads onto a
// multi-worker tier. The source's extra worker spread must not drag a
// control-plane-only consumer into the pin path (where it has no consumer worker
// and would be wrongly rejected): the consumer's node is fixed, so the
// deterministic node-equality check soundly matches it against the source's
// control-plane replica.
func TestResolveColocation_ControlPlaneConsumerWithMultiWorkerSource(t *testing.T) {
	store := dbtest.New(t)

	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir()},
		Runtime: config.RuntimeConfig{Tiers: []config.TierConfig{
			{Name: "local", Runtime: "native"},
			{Name: "burst", Runtime: "docker"},
		}},
	}
	srv := New(cfg, store, nil, nil)

	if err := store.CreateUser(db.CreateUserParams{
		Username: "owner", PasswordHash: "hash", Role: "admin",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	owner, _ := store.GetUserByUsername("owner")
	for _, slug := range []string{"source", "consumer"} {
		if err := store.CreateApp(db.CreateAppParams{
			Slug: slug, Name: slug, OwnerID: owner.ID, Access: "private",
		}); err != nil {
			t.Fatalf("create app %q: %v", slug, err)
		}
	}
	source, _ := store.GetAppBySlug("source")
	consumer, _ := store.GetAppBySlug("consumer")

	// Source spans the control-plane tier and a multi-worker tier; consumer is on
	// the control-plane tier only.
	srcPl, _ := json.Marshal(map[string]int{"local": 1, "burst": 1})
	if err := store.SetAppPlacement(source.ID, string(srcPl), 2); err != nil {
		t.Fatalf("set source placement: %v", err)
	}
	conPl, _ := json.Marshal(map[string]int{"local": 1})
	if err := store.SetAppPlacement(consumer.ID, string(conPl), 1); err != nil {
		t.Fatalf("set consumer placement: %v", err)
	}
	if err := store.GrantSharedData(consumer.ID, source.ID); err != nil {
		t.Fatalf("grant shared data: %v", err)
	}

	// burst is genuinely multi-worker (two up workers).
	reg, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	for _, addr := range []string{"10.0.0.1:8443", "10.0.0.2:8443"} {
		w, err := reg.Register(worker.RegisterParams{
			Name: addr, AdvertiseAddr: addr, Tier: "burst", Fingerprint: addr,
		})
		if err != nil {
			t.Fatalf("register worker %q: %v", addr, err)
		}
		// A worker is routable only after its first heartbeat (Register -> joining).
		if _, _, err := reg.Heartbeat(w.NodeID, addr, 0); err != nil {
			t.Fatalf("heartbeat worker %q: %v", addr, err)
		}
	}
	srv.SetWorkerRegistry(reg)
	srv.SetNodeForTier(func(tier string) string {
		if w, ok := reg.WorkerForTier(tier); ok {
			return w.NodeID
		}
		return ""
	})

	consumer, _ = store.GetAppBySlug("consumer")
	pins, err := srv.resolveColocation(consumer.ID, srv.tiersForApp(consumer))
	if err != nil {
		t.Fatalf("resolveColocation: want nil error for a control-plane consumer, got %v", err)
	}
	if pins != nil {
		t.Errorf("pins = %v, want nil: a control-plane consumer needs no worker pin", pins)
	}
	if err := srv.checkColocatedShared(consumer.ID, srv.tiersForApp(consumer)); err != nil {
		t.Errorf("checkColocatedShared: want nil for a feasible local-to-local mount, got %v", err)
	}
}

// TestMaybeRestartForChange_RejectsInfeasibleColocationBeforeTeardown asserts
// that an env-var-triggered restart (?restart=true) runs the colocation precheck
// before tearing down the old pool: when a shared-mount consumer cannot be
// co-located (its source is not running on any worker), the restart aborts with
// the conflict, leaves the running pool intact, and never reaches the redeploy.
// Without the precheck this path stops/deregisters the old pool and then deploys
// with no colocation pin, landing the consumer away from its source data.
func TestMaybeRestartForChange_RejectsInfeasibleColocationBeforeTeardown(t *testing.T) {
	store := dbtest.New(t)

	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir()},
		Runtime: config.RuntimeConfig{Tiers: []config.TierConfig{
			{Name: "local", Runtime: "native"},
			{Name: "burst", Runtime: "docker"},
		}},
	}
	srv := New(cfg, store, process.NewManager(t.TempDir(), process.NewNativeRuntime()), nil)

	if err := store.CreateUser(db.CreateUserParams{
		Username: "owner", PasswordHash: "hash", Role: "admin",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	owner, _ := store.GetUserByUsername("owner")
	for _, slug := range []string{"source", "consumer"} {
		if err := store.CreateApp(db.CreateAppParams{
			Slug: slug, Name: slug, OwnerID: owner.ID, Access: "private",
		}); err != nil {
			t.Fatalf("create app %q: %v", slug, err)
		}
	}
	source, _ := store.GetAppBySlug("source")
	consumer, _ := store.GetAppBySlug("consumer")

	// Both on the multi-worker burst tier; consumer mounts source; source is not
	// running anywhere, so colocation is infeasible.
	burst, _ := json.Marshal(map[string]int{"burst": 1})
	if err := store.SetAppPlacement(source.ID, string(burst), 1); err != nil {
		t.Fatalf("set source placement: %v", err)
	}
	if err := store.SetAppPlacement(consumer.ID, string(burst), 1); err != nil {
		t.Fatalf("set consumer placement: %v", err)
	}
	if err := store.GrantSharedData(consumer.ID, source.ID); err != nil {
		t.Fatalf("grant shared data: %v", err)
	}
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: consumer.ID, Version: "v1", BundleDir: t.TempDir(),
	}); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "consumer", Status: "running"}); err != nil {
		t.Fatalf("set running: %v", err)
	}

	reg, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	for _, addr := range []string{"10.0.0.1:8443", "10.0.0.2:8443"} {
		w, err := reg.Register(worker.RegisterParams{
			Name: addr, AdvertiseAddr: addr, Tier: "burst", Fingerprint: addr,
		})
		if err != nil {
			t.Fatalf("register worker %q: %v", addr, err)
		}
		// A worker is routable only after its first heartbeat (Register -> joining).
		if _, _, err := reg.Heartbeat(w.NodeID, addr, 0); err != nil {
			t.Fatalf("heartbeat worker %q: %v", addr, err)
		}
	}
	srv.SetWorkerRegistry(reg)
	srv.SetNodeForTier(func(tier string) string {
		if w, ok := reg.WorkerForTier(tier); ok {
			return w.NodeID
		}
		return ""
	})

	deployed := false
	srv.SetDeployRunForTest(func(deploy.Params) (*deploy.PoolResult, error) {
		deployed = true
		return &deploy.PoolResult{}, nil
	})

	consumer, _ = store.GetAppBySlug("consumer")
	req := httptest.NewRequest(http.MethodPut, "/app/consumer/env/X?restart=true", nil)
	restarted, err := srv.maybeRestartForChange(req, consumer, "consumer")
	if err == nil {
		t.Fatal("maybeRestartForChange returned nil error despite infeasible colocation")
	}
	if restarted {
		t.Error("maybeRestartForChange reported a restart despite infeasible colocation")
	}
	if deployed {
		t.Error("redeploy ran despite infeasible colocation: the pool was torn down before the precheck")
	}
	got, _ := store.GetAppBySlug("consumer")
	if got.Status != "running" {
		t.Errorf("app status = %q after an aborted restart; want running (left intact)", got.Status)
	}
}

// TestWithTierPlacement_SetsColocateWorkers asserts the pin computed by
// resolveColocation is threaded onto deploy.Params for every deploy path, so the
// pool launcher confines the consumer's replicas to the source-hosting worker.
func TestWithTierPlacement_SetsColocateWorkers(t *testing.T) {
	srv, source, consumer, _, nodeB := multiWorkerColocationFixture(t)
	seedRunningReplica(t, srv, source.ID, 0, nodeB)

	p := srv.withTierPlacement(deploy.Params{Slug: "consumer"}, consumer)
	if len(p.ColocateWorkers) != 1 || p.ColocateWorkers[0] != nodeB {
		t.Errorf("ColocateWorkers = %v, want [%s]", p.ColocateWorkers, nodeB)
	}
}

// TestColocationPins_ExposesPinForWatchdog asserts the exported best-effort pin
// accessor returns the source-hosting worker set so the lifecycle watchdog's
// single-replica restart pins a recovered replica to the same workers the full
// deploy uses, and returns nil (no constraint) when colocation is infeasible.
func TestColocationPins_ExposesPinForWatchdog(t *testing.T) {
	srv, source, consumer, nodeA, _ := multiWorkerColocationFixture(t)
	seedRunningReplica(t, srv, source.ID, 0, nodeA)

	pins := srv.ColocationPins(consumer)
	if len(pins) != 1 || pins[0] != nodeA {
		t.Errorf("ColocationPins = %v, want [%s]", pins, nodeA)
	}

	// With the source not running anywhere, colocation is infeasible; the
	// best-effort accessor returns nil rather than surfacing the error so the
	// watchdog falls back to unconstrained placement instead of wedging recovery.
	if err := srv.store.UpdateReplicaStatus(source.ID, 0, db.ReplicaStatusLost); err != nil {
		t.Fatalf("mark source replica lost: %v", err)
	}
	if pins := srv.ColocationPins(consumer); pins != nil {
		t.Errorf("ColocationPins (infeasible) = %v, want nil", pins)
	}
}

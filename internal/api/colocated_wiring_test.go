package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/worker"
)

func TestCheckColocatedSharedWiring(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

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

// TestCheckColocatedShared_RejectsMultiWorkerTier asserts the colocation check
// fails closed when a shared mount touches a tier backed by more than one up
// worker. The single-node tier->node mapping the colocation guard relies on
// cannot guarantee the source and consumer land on the same worker when a tier
// has several, so such a deploy must be rejected until same-worker pinning
// lands, rather than silently passing because both resolve to the first worker.
func TestCheckColocatedShared_RejectsMultiWorkerTier(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

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

	// Both apps on burst, so the single-node colocation check would pass: the
	// only thing that should reject this deploy is the multi-worker guard.
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
		if _, err := reg.Register(worker.RegisterParams{
			Name: addr, AdvertiseAddr: addr, Tier: "burst", Fingerprint: addr,
		}); err != nil {
			t.Fatalf("register worker %q: %v", addr, err)
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
	err = srv.checkColocatedShared(consumer.ID, srv.tiersForApp(consumer))
	if err == nil {
		t.Fatal("multi-worker tier with shared mount: want error, got nil")
	}
	if !strings.Contains(err.Error(), "burst") || !strings.Contains(err.Error(), "multi-worker") {
		t.Errorf("error should name the multi-worker tier; got: %v", err)
	}
}

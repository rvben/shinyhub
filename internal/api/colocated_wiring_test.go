package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
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

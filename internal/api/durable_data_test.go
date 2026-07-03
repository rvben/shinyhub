package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/process"
)

// ephemeralFakeRuntime is a Runtime whose tier storage is ephemeral (like bare
// Fargate), used to exercise the durable-data guard end to end.
type ephemeralFakeRuntime struct{ *manifestFakeRuntime }

func (ephemeralFakeRuntime) TierHasDurableData() bool { return false }

func newEphemeralTierServer(t *testing.T) (*Server, *db.Store) {
	t.Helper()
	appsDir := t.TempDir()
	store := dbtest.New(t)
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: appsDir, AppDataDir: t.TempDir()},
	}
	mgr := process.NewManager(appsDir, ephemeralFakeRuntime{newManifestFakeRuntime()})
	return New(cfg, store, mgr, nil), store
}

func mustGuardApp(t *testing.T, store *db.Store) *db.App {
	t.Helper()
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	u, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("myapp")
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestEphemeralDataDeployBlock_DataUsingAppOnEphemeralTierBlocked(t *testing.T) {
	srv, store := newEphemeralTierServer(t)
	app := mustGuardApp(t, store)
	cmd := []string{"uv", "run", "shiny", "run", "--data", "{data_dir}", "app.py"}
	tier, blocked, err := srv.ephemeralDataDeployBlock(app, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !blocked {
		t.Fatal("data-using app on ephemeral tier: want blocked, got allowed")
	}
	if tier == "" {
		t.Fatal("blocked but no tier named")
	}
}

func TestEphemeralDataDeployBlock_StatelessAppAllowed(t *testing.T) {
	srv, store := newEphemeralTierServer(t)
	app := mustGuardApp(t, store)
	cmd := []string{"uv", "run", "shiny", "run", "app.py"} // no {data_dir}, no pushed data
	_, blocked, err := srv.ephemeralDataDeployBlock(app, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if blocked {
		t.Fatal("stateless app on ephemeral tier: want allowed, got blocked")
	}
}

func TestEphemeralDataDeployBlock_AckAllows(t *testing.T) {
	srv, store := newEphemeralTierServer(t)
	app := mustGuardApp(t, store)
	if err := store.UpdateAppEphemeralDataAck(app.ID, true); err != nil {
		t.Fatal(err)
	}
	app, _ = store.GetAppBySlug("myapp")
	cmd := []string{"uv", "run", "shiny", "run", "--data", "{data_dir}", "app.py"}
	_, blocked, err := srv.ephemeralDataDeployBlock(app, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if blocked {
		t.Fatal("acknowledged app: want allowed, got blocked")
	}
}

func TestEphemeralDataBlockForTiers_BlocksDataOnEphemeralNewTiers(t *testing.T) {
	srv, store := newEphemeralTierServer(t)
	app := mustGuardApp(t, store)
	// Give the app data on disk so UsesPersistentData fires without a command.
	dir := filepath.Join(srv.cfg.Storage.AppDataDir, app.Slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.csv"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Moving this data-using app onto an ephemeral tier must be blocked.
	tier, blocked, err := srv.ephemeralDataBlockForTiers(app, nil, []string{"cloud"})
	if err != nil {
		t.Fatal(err)
	}
	if !blocked || tier == "" {
		t.Fatalf("data-using app on ephemeral new tier: want blocked, got blocked=%v tier=%q", blocked, tier)
	}

	// A stateless app (no data, no command) is allowed.
	if err := store.CreateApp(db.CreateAppParams{Slug: "stateless", Name: "S", OwnerID: app.OwnerID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	s2, _ := store.GetAppBySlug("stateless")
	if _, blocked, _ := srv.ephemeralDataBlockForTiers(s2, nil, []string{"cloud"}); blocked {
		t.Fatal("stateless app: want allowed on ephemeral tier")
	}
}

func TestEphemeralDataPushBlock_BlockedWithoutAck(t *testing.T) {
	srv, store := newEphemeralTierServer(t)
	app := mustGuardApp(t, store)
	if _, blocked := srv.ephemeralDataPushBlock(app); !blocked {
		t.Fatal("push to ephemeral tier without ack: want blocked, got allowed")
	}
	if err := store.UpdateAppEphemeralDataAck(app.ID, true); err != nil {
		t.Fatal(err)
	}
	app, _ = store.GetAppBySlug("myapp")
	if _, blocked := srv.ephemeralDataPushBlock(app); blocked {
		t.Fatal("push with ack: want allowed, got blocked")
	}
}

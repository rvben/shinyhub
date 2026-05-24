package db_test

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

func TestOpenAndMigrate(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}

func TestMigrate_FreshDBPopulatesLedger(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var n int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if n == 0 {
		t.Fatal("schema_migrations is empty after fresh migrate")
	}
	// Ledger version 1 must be recorded and the core table must exist.
	var name string
	if err := store.DB().QueryRow(
		`SELECT name FROM schema_migrations WHERE version=1`).Scan(&name); err != nil {
		t.Fatalf("version 1 not recorded: %v", err)
	}
	if err := store.DB().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='users'`).Scan(&name); err != nil {
		t.Fatalf("users table missing after migrate: %v", err)
	}
}

// TestMigrate_BaselinesLegacyDB proves a fully-migrated database that predates
// the ledger (the original runner left no schema_migrations table) is adopted
// without error and without destroying data, rather than re-running 001+.
func TestMigrate_BaselinesLegacyDB(t *testing.T) {
	dsn := t.TempDir() + "/legacy.db"
	store, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}
	if err := store.CreateUser(db.CreateUserParams{
		Username: "legacy", PasswordHash: "h", Role: "admin",
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// Simulate a pre-ledger database.
	if _, err := store.DB().Exec(`DROP TABLE schema_migrations`); err != nil {
		t.Fatalf("drop ledger: %v", err)
	}

	if err := store.Migrate(); err != nil {
		t.Fatalf("baseline migrate: %v", err)
	}
	// Ledger restored.
	var n int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if n == 0 {
		t.Fatal("ledger not restored after baseline")
	}
	// Data preserved (proves migrations were not destructively re-run).
	u, err := store.GetUserByUsername("legacy")
	if err != nil || u.Username != "legacy" {
		t.Fatalf("seeded user lost across baseline: %v %+v", err, u)
	}
}

func TestCreateAndGetUser(t *testing.T) {
	store := mustOpenDB(t)
	err := store.CreateUser(db.CreateUserParams{
		Username:     "alice",
		PasswordHash: "hash",
		Role:         "admin",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	u, err := store.GetUserByUsername("alice")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if u.Username != "alice" || u.Role != "admin" {
		t.Errorf("unexpected user: %+v", u)
	}
}

func TestCreateAndGetApp(t *testing.T) {
	store := mustOpenDB(t)
	// Create the owning user first (FK requires it)
	if err := store.CreateUser(db.CreateUserParams{
		Username:     "owner",
		PasswordHash: "hash",
		Role:         "developer",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatalf("get owner: %v", err)
	}
	if err := store.CreateApp(db.CreateAppParams{
		Slug:        "my-app",
		Name:        "My App",
		ProjectSlug: "default",
		OwnerID:     owner.ID,
	}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app, err := store.GetAppBySlug("my-app")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.Slug != "my-app" {
		t.Errorf("expected slug my-app, got %s", app.Slug)
	}
}

func mustOpenDB(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// openTestStore is an alias for mustOpenDB used in resource-limit tests.
func openTestStore(t *testing.T) *db.Store {
	t.Helper()
	return mustOpenDB(t)
}

func mustCreateUser(t *testing.T, s *db.Store, name, role string) *db.User {
	t.Helper()
	if err := s.CreateUser(db.CreateUserParams{Username: name, PasswordHash: "h", Role: role}); err != nil {
		t.Fatalf("create user %q: %v", name, err)
	}
	u, err := s.GetUserByUsername(name)
	if err != nil {
		t.Fatalf("get user %q: %v", name, err)
	}
	return u
}

func mustCreateApp(t *testing.T, s *db.Store, slug string, ownerID int64) *db.App {
	t.Helper()
	if err := s.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: ownerID, Access: "private"}); err != nil {
		t.Fatalf("create app %q: %v", slug, err)
	}
	app, err := s.GetAppBySlug(slug)
	if err != nil {
		t.Fatalf("get app %q: %v", slug, err)
	}
	return app
}

func mustDeleteApp(t *testing.T, s *db.Store, slug string) {
	t.Helper()
	if err := s.DeleteApp(slug); err != nil {
		t.Fatalf("delete app %q: %v", slug, err)
	}
}

func TestMigrate_HibernateTimeoutColumn(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if err := store.CreateUser(db.CreateUserParams{Username: "u", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, err := store.GetUserByUsername("u")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}

	mins := 45
	if _, _, err := store.PatchAppSettings(db.PatchAppSettingsParams{
		Slug: "myapp", SetHibernate: true, HibernateMinutes: &mins,
	}); err != nil {
		t.Fatalf("PatchAppSettings hibernate: %v", err)
	}
	app, err := store.GetAppBySlug("myapp")
	if err != nil {
		t.Fatal(err)
	}
	if app.HibernateTimeoutMinutes == nil || *app.HibernateTimeoutMinutes != 45 {
		t.Errorf("expected HibernateTimeoutMinutes=45, got %v", app.HibernateTimeoutMinutes)
	}

	// Reset to NULL (global default).
	if _, _, err := store.PatchAppSettings(db.PatchAppSettingsParams{
		Slug: "myapp", SetHibernate: true, HibernateMinutes: nil,
	}); err != nil {
		t.Fatalf("PatchAppSettings hibernate nil: %v", err)
	}
	app, err = store.GetAppBySlug("myapp")
	if err != nil {
		t.Fatal(err)
	}
	if app.HibernateTimeoutMinutes != nil {
		t.Errorf("expected nil after reset, got %v", app.HibernateTimeoutMinutes)
	}
}

func TestAppMembers_GrantRevoke(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}

	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: "h", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.GetUserByUsername("owner")
	alice, _ := store.GetUserByUsername("alice")

	if err := store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}

	if err := store.GrantAppAccess("myapp", alice.ID); err != nil {
		t.Fatalf("GrantAppAccess: %v", err)
	}

	ok, err := store.UserCanAccessApp("myapp", alice.ID)
	if err != nil {
		t.Fatalf("UserCanAccessApp: %v", err)
	}
	if !ok {
		t.Error("expected alice to have access after grant")
	}

	if err := store.RevokeAppAccess("myapp", alice.ID); err != nil {
		t.Fatalf("RevokeAppAccess: %v", err)
	}
	ok, _ = store.UserCanAccessApp("myapp", alice.ID)
	if ok {
		t.Error("expected access revoked")
	}
}

func TestAppAccess_OwnerAlwaysHasAccess(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}

	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.GetUserByUsername("owner")
	if err := store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}

	ok, err := store.UserCanAccessApp("myapp", owner.ID)
	if err != nil {
		t.Fatalf("UserCanAccessApp: %v", err)
	}
	if !ok {
		t.Error("expected owner to always have access")
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	var before int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&before); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("second Migrate must be idempotent: %v", err)
	}
	var after int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&after); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if before == 0 || before != after {
		t.Errorf("ledger not stable across re-migrate: before=%d after=%d", before, after)
	}
}

func TestOAuthAccount_CreateAndLookup(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}

	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: "h", Role: "developer"})
	alice, _ := store.GetUserByUsername("alice")

	err = store.CreateOAuthAccount(db.CreateOAuthAccountParams{
		UserID:     alice.ID,
		Provider:   "github",
		ProviderID: "gh_123",
	})
	if err != nil {
		t.Fatalf("CreateOAuthAccount: %v", err)
	}

	u, err := store.GetUserByOAuthAccount("github", "gh_123")
	if err != nil {
		t.Fatalf("GetUserByOAuthAccount: %v", err)
	}
	if u.Username != "alice" {
		t.Errorf("expected alice, got %s", u.Username)
	}
}

func TestGetAppMembers_ReturnsUsernameAndRole(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}

	// Create owner and member users.
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "", Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: "", Role: "viewer"})
	owner, _ := store.GetUserByUsername("owner")
	alice, _ := store.GetUserByUsername("alice")

	// Create app and grant alice access.
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: owner.ID})
	store.GrantAppAccess("myapp", alice.ID)

	members, err := store.GetAppMembers("myapp")
	if err != nil {
		t.Fatalf("GetAppMembers: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("expected 1 member, got %d", len(members))
	}
	if members[0].UserID != alice.ID {
		t.Errorf("UserID = %d, want %d", members[0].UserID, alice.ID)
	}
	if members[0].Username != "alice" {
		t.Errorf("Username = %q, want %q", members[0].Username, "alice")
	}
	if members[0].Role != "viewer" {
		t.Errorf("Role = %q, want %q", members[0].Role, "viewer")
	}
}

func TestOAuthState_ConsumeOnce(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate()

	if err := store.CreateOAuthState("nonce-abc123"); err != nil {
		t.Fatalf("CreateOAuthState: %v", err)
	}

	// First consume: should succeed.
	if err := store.ConsumeOAuthState("nonce-abc123"); err != nil {
		t.Errorf("first ConsumeOAuthState failed: %v", err)
	}

	// Second consume: state is gone, should fail.
	if err := store.ConsumeOAuthState("nonce-abc123"); err == nil {
		t.Error("expected error on second ConsumeOAuthState, got nil")
	}
}

func TestOAuthState_ExpiredStateIsRejected(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate()

	// Create two states: one fresh, one that will be backdated.
	if err := store.CreateOAuthState("nonce-fresh"); err != nil {
		t.Fatalf("CreateOAuthState fresh: %v", err)
	}
	if err := store.CreateOAuthState("nonce-stale"); err != nil {
		t.Fatalf("CreateOAuthState stale: %v", err)
	}

	// Backdate nonce-stale to 15 minutes ago.
	_, err = store.DB().Exec(
		`UPDATE oauth_states SET created_at = datetime('now', '-15 minutes') WHERE state = 'nonce-stale'`)
	if err != nil {
		t.Fatalf("backdate: %v", err)
	}

	// Consuming the fresh state triggers the sweep and must succeed.
	if err := store.ConsumeOAuthState("nonce-fresh"); err != nil {
		t.Fatalf("ConsumeOAuthState fresh: %v", err)
	}

	// The sweep that ran during nonce-fresh consume should have deleted nonce-stale.
	// Consuming it now must fail.
	if err := store.ConsumeOAuthState("nonce-stale"); err == nil {
		t.Error("expected error consuming expired state, got nil")
	}
}

func TestGetDeploymentBySlugAndID(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}

	if err := store.CreateUser(db.CreateUserParams{Username: "u", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, err := store.GetUserByUsername("u")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("myapp")
	if err != nil {
		t.Fatal(err)
	}

	dep, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v1", BundleDir: "/tmp/v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.GetDeploymentBySlugAndID("myapp", dep.ID)
	if err != nil {
		t.Fatalf("GetDeploymentBySlugAndID: %v", err)
	}
	if got.BundleDir != "/tmp/v1" {
		t.Errorf("got BundleDir=%s, want /tmp/v1", got.BundleDir)
	}

	_, err = store.GetDeploymentBySlugAndID("myapp", 9999)
	if !errors.Is(err, db.ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing ID, got %v", err)
	}

	_, err = store.GetDeploymentBySlugAndID("wrongslug", dep.ID)
	if !errors.Is(err, db.ErrNotFound) {
		t.Errorf("expected ErrNotFound for wrong slug, got %v", err)
	}
}

func TestAuditLog(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}

	if err := store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, err := store.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}

	store.LogAuditEvent(db.AuditEventParams{
		UserID:       &u.ID,
		Action:       "deploy",
		ResourceType: "app",
		ResourceID:   "myapp",
		IPAddress:    "1.2.3.4",
	})
	store.LogAuditEvent(db.AuditEventParams{
		Action:       "login_failed",
		ResourceType: "user",
		ResourceID:   "unknown",
		IPAddress:    "5.6.7.8",
	})

	events, err := store.ListAuditEvents("", 10, 0)
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	// Newest first.
	if events[0].Action != "login_failed" {
		t.Errorf("expected newest event first, got action=%s", events[0].Action)
	}
	if events[1].Action != "deploy" {
		t.Errorf("expected deploy second, got %s", events[1].Action)
	}
	if events[1].ResourceID != "myapp" {
		t.Errorf("expected myapp, got %s", events[1].ResourceID)
	}
	// CreatedAt must be populated — not the zero value.
	for i, e := range events {
		if e.CreatedAt.IsZero() {
			t.Errorf("event[%d] CreatedAt is zero", i)
		}
	}
}

func TestPatchAppSettings_ResourceLimits(t *testing.T) {
	store := openTestStore(t)

	err := store.CreateUser(db.CreateUserParams{
		Username: "owner", PasswordHash: "x", Role: "developer",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	user, _ := store.GetUserByUsername("owner")

	err = store.CreateApp(db.CreateAppParams{
		Slug: "test-app", Name: "Test", OwnerID: user.ID,
	})
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	memMB := 512
	cpuPct := 75
	if _, _, err := store.PatchAppSettings(db.PatchAppSettingsParams{
		Slug:               "test-app",
		SetMemoryLimitMB:   true,
		MemoryLimitMB:      &memMB,
		SetCPUQuotaPercent: true,
		CPUQuotaPercent:    &cpuPct,
	}); err != nil {
		t.Fatalf("PatchAppSettings limits: %v", err)
	}

	app, err := store.GetApp("test-app")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if app.MemoryLimitMB == nil || *app.MemoryLimitMB != 512 {
		t.Errorf("expected MemoryLimitMB=512, got %v", app.MemoryLimitMB)
	}
	if app.CPUQuotaPercent == nil || *app.CPUQuotaPercent != 75 {
		t.Errorf("expected CPUQuotaPercent=75, got %v", app.CPUQuotaPercent)
	}

	// Updating only memory must preserve the existing CPU limit.
	memMB2 := 256
	if _, _, err := store.PatchAppSettings(db.PatchAppSettingsParams{
		Slug:             "test-app",
		SetMemoryLimitMB: true,
		MemoryLimitMB:    &memMB2,
	}); err != nil {
		t.Fatalf("PatchAppSettings memory-only: %v", err)
	}
	app, _ = store.GetApp("test-app")
	if app.MemoryLimitMB == nil || *app.MemoryLimitMB != 256 {
		t.Errorf("expected MemoryLimitMB=256, got %v", app.MemoryLimitMB)
	}
	if app.CPUQuotaPercent == nil || *app.CPUQuotaPercent != 75 {
		t.Errorf("expected CPUQuotaPercent preserved at 75, got %v", app.CPUQuotaPercent)
	}

	// Setting to nil should clear the limits.
	if _, _, err := store.PatchAppSettings(db.PatchAppSettingsParams{
		Slug:               "test-app",
		SetMemoryLimitMB:   true,
		MemoryLimitMB:      nil,
		SetCPUQuotaPercent: true,
		CPUQuotaPercent:    nil,
	}); err != nil {
		t.Fatalf("PatchAppSettings (clear): %v", err)
	}
	app, _ = store.GetApp("test-app")
	if app.MemoryLimitMB != nil {
		t.Errorf("expected nil MemoryLimitMB after clear, got %v", app.MemoryLimitMB)
	}

	// PatchAppSettings on a non-existent slug must return ErrNotFound.
	_, _, err = store.PatchAppSettings(db.PatchAppSettingsParams{Slug: "no-such-app"})
	if !errors.Is(err, db.ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing slug, got %v", err)
	}
}

func TestListAuditEvents_UsernameJoin(t *testing.T) {
	store := mustOpenDB(t)
	if err := store.CreateUser(db.CreateUserParams{
		Username: "alice", PasswordHash: "h", Role: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByUsername("alice")
	store.LogAuditEvent(db.AuditEventParams{
		UserID: &u.ID, Action: "deploy", ResourceType: "app", ResourceID: "myapp",
	})
	events, err := store.ListAuditEvents("", 10, 0)
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Username == nil || *events[0].Username != "alice" {
		t.Errorf("expected username=alice, got %v", events[0].Username)
	}
}

func TestListAuditEvents_NilUserHasNilUsername(t *testing.T) {
	store := mustOpenDB(t)
	store.LogAuditEvent(db.AuditEventParams{
		Action: "login_failed", ResourceType: "user", ResourceID: "unknown",
	})
	events, err := store.ListAuditEvents("", 10, 0)
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Username != nil {
		t.Errorf("expected nil username for anonymous event, got %v", *events[0].Username)
	}
}

func TestAppEnvVars_UpsertListDelete(t *testing.T) {
	store := mustOpenDB(t)

	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.GetUserByUsername("owner")
	if err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("demo")

	// Insert non-secret var.
	if err := store.UpsertAppEnvVar(app.ID, "AWS_REGION", []byte("eu-west-1"), false); err != nil {
		t.Fatalf("insert non-secret: %v", err)
	}
	// Insert secret var.
	if err := store.UpsertAppEnvVar(app.ID, "AWS_SECRET", []byte("ciphertext-blob"), true); err != nil {
		t.Fatalf("insert secret: %v", err)
	}

	// List — expect both vars.
	vars, err := store.ListAppEnvVars(app.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(vars) != 2 {
		t.Fatalf("want 2 vars, got %d", len(vars))
	}
	// Ordered by key: AWS_REGION < AWS_SECRET.
	if vars[0].Key != "AWS_REGION" {
		t.Errorf("want first key AWS_REGION, got %s", vars[0].Key)
	}
	if vars[1].Key != "AWS_SECRET" {
		t.Errorf("want second key AWS_SECRET, got %s", vars[1].Key)
	}
	if vars[1].IsSecret != true {
		t.Errorf("want IsSecret=true for AWS_SECRET")
	}
	if vars[0].IsSecret != false {
		t.Errorf("want IsSecret=false for AWS_REGION")
	}
	if vars[0].CreatedAt.IsZero() {
		t.Error("CreatedAt must not be zero")
	}

	// Update via upsert.
	if err := store.UpsertAppEnvVar(app.ID, "AWS_REGION", []byte("us-east-1"), false); err != nil {
		t.Fatalf("update: %v", err)
	}
	v, err := store.GetAppEnvVar(app.ID, "AWS_REGION")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(v.Value) != "us-east-1" {
		t.Errorf("want us-east-1, got %s", v.Value)
	}

	// CountAppEnvVars.
	n, err := store.CountAppEnvVars(app.ID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("want count=2, got %d", n)
	}

	// Delete one var.
	if err := store.DeleteAppEnvVar(app.ID, "AWS_REGION"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	vars, _ = store.ListAppEnvVars(app.ID)
	if len(vars) != 1 {
		t.Errorf("want 1 after delete, got %d", len(vars))
	}

	// Delete non-existent key must return sql.ErrNoRows.
	err = store.DeleteAppEnvVar(app.ID, "NO_SUCH_KEY")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("want sql.ErrNoRows for missing key, got %v", err)
	}
}

func TestAppEnvVars_CascadeOnAppDelete(t *testing.T) {
	store := mustOpenDB(t)

	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.GetUserByUsername("owner")
	if err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("demo")

	if err := store.UpsertAppEnvVar(app.ID, "FOO", []byte("bar"), false); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := store.DeleteApp(app.Slug); err != nil {
		t.Fatalf("delete app: %v", err)
	}

	vars, err := store.ListAppEnvVars(app.ID)
	if err != nil {
		t.Fatalf("list after cascade: %v", err)
	}
	if len(vars) != 0 {
		t.Errorf("expected cascade delete, got %d vars", len(vars))
	}
}

func TestListRunningApps(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}

	store.CreateUser(db.CreateUserParams{Username: "u", PasswordHash: "h", Role: "admin"})
	u, _ := store.GetUserByUsername("u")
	store.CreateApp(db.CreateAppParams{Slug: "app1", Name: "App 1", OwnerID: u.ID})
	store.CreateApp(db.CreateAppParams{Slug: "app2", Name: "App 2", OwnerID: u.ID})

	store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "app1", Status: "running"})
	// app2 remains "stopped"

	apps, err := store.ListRunningApps()
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 {
		t.Fatalf("expected 1 running app, got %d", len(apps))
	}
	if apps[0].Slug != "app1" {
		t.Errorf("expected app1, got %s", apps[0].Slug)
	}
	if apps[0].Replicas != 1 {
		t.Errorf("expected default Replicas=1, got %d", apps[0].Replicas)
	}
}

func TestApp_HasReplicasColumn(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "o", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)
	if app.Replicas != 1 {
		t.Fatalf("expected default Replicas=1, got %d", app.Replicas)
	}
}

func TestReplicas_UpsertListDelete(t *testing.T) {
	store := mustOpenDB(t)
	user := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", user.ID)

	pid, port := 111, 20001
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, PID: &pid, Port: &port, Status: "running",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	pid2, port2 := 222, 20002
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 1, PID: &pid2, Port: &port2, Status: "running",
	}); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}

	reps, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 2 {
		t.Fatalf("want 2 replicas, got %d", len(reps))
	}
	if reps[0].Index != 0 || reps[1].Index != 1 {
		t.Fatalf("want ordered [0,1], got [%d,%d]", reps[0].Index, reps[1].Index)
	}

	// Idempotent upsert.
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: "stopped",
	}); err != nil {
		t.Fatal(err)
	}
	reps, _ = store.ListReplicas(app.ID)
	if reps[0].Status != "stopped" {
		t.Fatalf("want stopped, got %s", reps[0].Status)
	}

	// Delete one.
	if err := store.DeleteReplica(app.ID, 1); err != nil {
		t.Fatal(err)
	}
	reps, _ = store.ListReplicas(app.ID)
	if len(reps) != 1 {
		t.Fatalf("want 1 after delete, got %d", len(reps))
	}

	// Cascade delete with app.
	mustDeleteApp(t, store, app.Slug)
	reps, _ = store.ListReplicas(app.ID)
	if len(reps) != 0 {
		t.Fatalf("cascade delete: want 0, got %d", len(reps))
	}
}

// TestPatchAppSettings_ReplicaShrinkPrune asserts that lowering the replica
// count prunes the obsolete replica rows (idx >= new count) in the same
// transaction, so ListReplicas stays consistent with the target count.
func TestPatchAppSettings_ReplicaShrinkPrune(t *testing.T) {
	store := mustOpenDB(t)
	user := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", user.ID)

	for _, idx := range []int{0, 1, 2} {
		if err := store.UpsertReplica(db.UpsertReplicaParams{
			AppID: app.ID, Index: idx, Status: "stopped",
		}); err != nil {
			t.Fatalf("upsert replica %d: %v", idx, err)
		}
	}
	// Record a target of 3 so the subsequent shrink is a true shrink.
	if _, _, err := store.PatchAppSettings(db.PatchAppSettingsParams{
		Slug: "demo", SetReplicas: true, Replicas: 3,
	}); err != nil {
		t.Fatalf("PatchAppSettings grow to 3: %v", err)
	}

	// Shrink to 2 removes idx >= 2, leaving [0, 1].
	if _, _, err := store.PatchAppSettings(db.PatchAppSettingsParams{
		Slug: "demo", SetReplicas: true, Replicas: 2,
	}); err != nil {
		t.Fatalf("PatchAppSettings shrink to 2: %v", err)
	}
	reps, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 2 {
		t.Fatalf("want 2 replicas after shrink to 2, got %d", len(reps))
	}
	if reps[0].Index != 0 || reps[1].Index != 1 {
		t.Fatalf("want indices [0,1], got [%d,%d]", reps[0].Index, reps[1].Index)
	}

	// Shrink to 1 prunes idx >= 1, leaving only replica 0.
	if _, _, err := store.PatchAppSettings(db.PatchAppSettingsParams{
		Slug: "demo", SetReplicas: true, Replicas: 1,
	}); err != nil {
		t.Fatalf("PatchAppSettings shrink to 1: %v", err)
	}
	reps, err = store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 1 || reps[0].Index != 0 {
		t.Fatalf("want single replica index 0 after shrink to 1, got %+v", reps)
	}
}

// TestApp_DeploymentSummaryFields asserts that GetAppBySlug, GetAppByID and
// ListApps populate LastDeployedAt + CurrentVersion from the most recent
// deployment row. The grid sort and the detail-card "Deployed" line both
// depend on these fields; the SPA has no other source for either value.
//
// Regression guard: SQLite's MAX(created_at) aggregate strips column type
// metadata, so the driver returns a string. scanApp must parse it via
// parseSQLiteTime — if that path breaks, the apps queries return HTTP 500.
func TestApp_DeploymentSummaryFields(t *testing.T) {
	store := mustOpenDB(t)
	user := mustCreateUser(t, store, "owner", "developer")

	// App with no deployments — both fields should be zero values.
	never := mustCreateApp(t, store, "never", user.ID)
	if never.LastDeployedAt != nil {
		t.Errorf("never-deployed app: LastDeployedAt = %v, want nil", never.LastDeployedAt)
	}
	if never.CurrentVersion != "" {
		t.Errorf("never-deployed app: CurrentVersion = %q, want empty", never.CurrentVersion)
	}

	// App with two deployments — fields should reflect the most recent one.
	app := mustCreateApp(t, store, "demo", user.ID)
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v1", BundleDir: "/tmp/v1", Status: "succeeded",
	}); err != nil {
		t.Fatalf("create deploy v1: %v", err)
	}
	// SQLite CURRENT_TIMESTAMP has 1s resolution; sleep a smidge so the
	// MAX(created_at) aggregate can pick the v2 row deterministically.
	time.Sleep(1100 * time.Millisecond)
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v2", BundleDir: "/tmp/v2", Status: "succeeded",
	}); err != nil {
		t.Fatalf("create deploy v2: %v", err)
	}

	got, err := store.GetAppBySlug("demo")
	if err != nil {
		t.Fatalf("GetAppBySlug: %v", err)
	}
	if got.CurrentVersion != "v2" {
		t.Errorf("GetAppBySlug: CurrentVersion = %q, want v2", got.CurrentVersion)
	}
	if got.LastDeployedAt == nil {
		t.Fatalf("GetAppBySlug: LastDeployedAt = nil, want a parsed time")
	}
	if got.LastDeployedAt.IsZero() {
		t.Errorf("GetAppBySlug: LastDeployedAt is zero, want a parsed time")
	}

	// GetAppByID must return the same fields.
	byID, err := store.GetAppByID(app.ID)
	if err != nil {
		t.Fatalf("GetAppByID: %v", err)
	}
	if byID.CurrentVersion != "v2" {
		t.Errorf("GetAppByID: CurrentVersion = %q, want v2", byID.CurrentVersion)
	}
	if byID.LastDeployedAt == nil || byID.LastDeployedAt.IsZero() {
		t.Errorf("GetAppByID: LastDeployedAt = %v, want non-zero", byID.LastDeployedAt)
	}

	// ListApps must populate fields for every row.
	all, err := store.ListApps(100, 0)
	if err != nil {
		t.Fatalf("ListApps: %v", err)
	}
	var listedDemo, listedNever *db.App
	for i := range all {
		switch all[i].Slug {
		case "demo":
			listedDemo = all[i]
		case "never":
			listedNever = all[i]
		}
	}
	if listedDemo == nil || listedNever == nil {
		t.Fatalf("ListApps: missing demo or never; got %d apps", len(all))
	}
	if listedDemo.CurrentVersion != "v2" {
		t.Errorf("ListApps demo: CurrentVersion = %q, want v2", listedDemo.CurrentVersion)
	}
	if listedDemo.LastDeployedAt == nil || listedDemo.LastDeployedAt.IsZero() {
		t.Errorf("ListApps demo: LastDeployedAt = %v, want non-zero", listedDemo.LastDeployedAt)
	}
	if listedNever.LastDeployedAt != nil {
		t.Errorf("ListApps never: LastDeployedAt = %v, want nil", listedNever.LastDeployedAt)
	}
	if listedNever.CurrentVersion != "" {
		t.Errorf("ListApps never: CurrentVersion = %q, want empty", listedNever.CurrentVersion)
	}
}

func TestSetDeploymentDigest(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "digest-owner", "developer")
	app := mustCreateApp(t, store, "digest-app", owner.ID)

	dep, err := store.BeginDeployment(app.ID, "v1", "/tmp/bundle")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := store.SetDeploymentDigest(dep.ID, "sha256:abc"); err != nil {
		t.Fatalf("set digest: %v", err)
	}
	var got *string
	row := store.DB().QueryRow(`SELECT content_digest FROM deployments WHERE id = ?`, dep.ID)
	if err := row.Scan(&got); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got == nil || *got != "sha256:abc" {
		t.Fatalf("content_digest = %v, want sha256:abc", got)
	}
}

func TestUpsertSystemUser_CreatesThenUpdatesRole(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}

	u1, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if u1.Username != db.SystemUsernameDeploy || u1.Role != "developer" {
		t.Errorf("got %+v", u1)
	}

	u2, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "operator")
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if u2.ID != u1.ID {
		t.Errorf("upsert should preserve ID: got %d, want %d", u2.ID, u1.ID)
	}
	if u2.Role != "operator" {
		t.Errorf("role not updated: got %q", u2.Role)
	}
}

func TestIsSystemUser(t *testing.T) {
	if !db.IsSystemUser(db.SystemUsernameDeploy) {
		t.Error("__deploy__ should be a system user")
	}
	if db.IsSystemUser("alice") {
		t.Error("alice should not be a system user")
	}
}

func TestUpsertSystemUser_RejectsNonSystemUsername(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}

	_, err = store.UpsertSystemUser("alice", "developer")
	if err == nil {
		t.Fatal("expected error upserting a non-system username, got nil")
	}
	// And nothing got inserted.
	if _, getErr := store.GetUserByUsername("alice"); getErr == nil {
		t.Error("non-system user must not be created")
	}
}

func TestUpsertSystemUser_SameRoleIsIdempotent(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}

	u1, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	if err != nil {
		t.Fatal(err)
	}
	u2, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	if err != nil {
		t.Fatal(err)
	}
	if u2.ID != u1.ID || u2.Role != "developer" {
		t.Errorf("idempotent upsert: got %+v, want same ID and role developer", u2)
	}
}

func TestListAppsExposesDigestAndManagedBy(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner-digest", "developer")
	app := mustCreateApp(t, store, "exposed", owner.ID)

	dep, err := store.BeginDeployment(app.ID, "v1", "/tmp/b")
	if err != nil {
		t.Fatalf("begin deployment: %v", err)
	}
	if err := store.SetDeploymentDigest(dep.ID, "sha256:live"); err != nil {
		t.Fatalf("set deployment digest: %v", err)
	}
	if err := store.PromoteDeployment(dep.ID); err != nil {
		t.Fatalf("promote deployment: %v", err)
	}
	if _, err := store.DB().Exec(
		`UPDATE apps SET managed_by = ? WHERE id = ?`, "fleet:prod", app.ID); err != nil {
		t.Fatalf("set managed_by: %v", err)
	}

	apps, err := store.ListApps(0, 0)
	if err != nil {
		t.Fatalf("list apps: %v", err)
	}
	var found *db.App
	for i := range apps {
		if apps[i].Slug == "exposed" {
			found = apps[i]
		}
	}
	if found == nil {
		t.Fatal("app not listed")
	}
	if found.ContentDigest != "sha256:live" {
		t.Fatalf("ContentDigest = %q, want sha256:live", found.ContentDigest)
	}
	if found.ManagedBy == nil || *found.ManagedBy != "fleet:prod" {
		t.Fatalf("ManagedBy = %v, want fleet:prod", found.ManagedBy)
	}
}

func TestReplicaMetadataRoundTrip(t *testing.T) {
	store := openTestStore(t)
	owner := mustCreateUser(t, store, "meta-owner", "developer")
	app := mustCreateApp(t, store, "metadata-app", owner.ID)

	pid, port := 4242, 33001
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID:        app.ID,
		Index:        0,
		PID:          &pid,
		Port:         &port,
		Status:       "running",
		Provider:     "native",
		Tier:         "local",
		EndpointURL:  "http://127.0.0.1:33001",
		WorkerID:     "4242",
		AppVersion:   "v3",
		DesiredState: "running",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	reps, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(reps) != 1 {
		t.Fatalf("got %d replicas; want 1", len(reps))
	}
	r := reps[0]
	if r.Provider != "native" || r.Tier != "local" ||
		r.EndpointURL != "http://127.0.0.1:33001" || r.WorkerID != "4242" ||
		r.AppVersion != "v3" || r.DesiredState != "running" {
		t.Fatalf("metadata not round-tripped: %+v", r)
	}
}

func TestUpsertReplicaDefaultsDesiredStateToRunning(t *testing.T) {
	store := openTestStore(t)
	owner := mustCreateUser(t, store, "default-owner", "developer")
	app := mustCreateApp(t, store, "default-desired-app", owner.ID)

	// Caller omits DesiredState entirely (the live path for all current call sites).
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID:  app.ID,
		Index:  0,
		Status: "stopped",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	reps, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(reps) != 1 {
		t.Fatalf("got %d replicas; want 1", len(reps))
	}
	if reps[0].DesiredState != "running" {
		t.Fatalf("DesiredState = %q; want %q (empty input must default to running)", reps[0].DesiredState, "running")
	}
}

func TestListAppsDigestNilUntilPromoted(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner-pending", "developer")
	app := mustCreateApp(t, store, "pending-only", owner.ID)

	dep, err := store.BeginDeployment(app.ID, "v1", "/tmp/b")
	if err != nil {
		t.Fatalf("begin deployment: %v", err)
	}
	if err := store.SetDeploymentDigest(dep.ID, "sha256:notlive"); err != nil {
		t.Fatalf("set deployment digest: %v", err)
	}
	// pending, not promoted

	apps, err := store.ListApps(0, 0)
	if err != nil {
		t.Fatalf("list apps: %v", err)
	}
	for _, a := range apps {
		if a.Slug == "pending-only" && a.ContentDigest != "" {
			t.Fatalf("pending deployment digest must not be exposed, got %q", a.ContentDigest)
		}
	}
}

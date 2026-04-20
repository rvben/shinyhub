package db_test

import (
	"database/sql"
	"errors"
	"testing"

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
	if err := store.UpdateHibernateTimeout("myapp", &mins); err != nil {
		t.Fatalf("UpdateHibernateTimeout: %v", err)
	}
	app, err := store.GetAppBySlug("myapp")
	if err != nil {
		t.Fatal(err)
	}
	if app.HibernateTimeoutMinutes == nil || *app.HibernateTimeoutMinutes != 45 {
		t.Errorf("expected HibernateTimeoutMinutes=45, got %v", app.HibernateTimeoutMinutes)
	}

	// Reset to NULL (global default).
	if err := store.UpdateHibernateTimeout("myapp", nil); err != nil {
		t.Fatalf("UpdateHibernateTimeout nil: %v", err)
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
	if err := store.Migrate(); err != nil {
		t.Fatalf("second Migrate must be idempotent: %v", err)
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

	events, err := store.ListAuditEvents(10, 0)
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

func TestUpdateResourceLimits(t *testing.T) {
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
	err = store.UpdateResourceLimits(db.UpdateResourceLimitsParams{
		Slug:            "test-app",
		MemoryLimitMB:   &memMB,
		CPUQuotaPercent: &cpuPct,
	})
	if err != nil {
		t.Fatalf("UpdateResourceLimits: %v", err)
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

	// Setting to nil should clear the limits.
	err = store.UpdateResourceLimits(db.UpdateResourceLimitsParams{
		Slug:            "test-app",
		MemoryLimitMB:   nil,
		CPUQuotaPercent: nil,
	})
	if err != nil {
		t.Fatalf("UpdateResourceLimits (clear): %v", err)
	}
	app, _ = store.GetApp("test-app")
	if app.MemoryLimitMB != nil {
		t.Errorf("expected nil MemoryLimitMB after clear, got %v", app.MemoryLimitMB)
	}

	// UpdateResourceLimits on a non-existent slug must return ErrNotFound.
	err = store.UpdateResourceLimits(db.UpdateResourceLimitsParams{Slug: "no-such-app"})
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
	events, err := store.ListAuditEvents(10, 0)
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
	events, err := store.ListAuditEvents(10, 0)
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

	port, pid := 20001, 12345
	store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "app1", Status: "running", Port: &port, PID: &pid})
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
	if apps[0].CurrentPID == nil || *apps[0].CurrentPID != 12345 {
		t.Errorf("expected PID 12345, got %v", apps[0].CurrentPID)
	}
}

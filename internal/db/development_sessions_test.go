package db_test

import (
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

func TestDevelopmentSessionGroupsDeploymentsAndCannotBeReopened(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "app", owner.ID)
	id := "0123456789abcdef0123456789abcdef"
	params := db.UpsertDevelopmentSessionParams{
		ID: id, AppID: app.ID, TargetKind: db.DevelopmentTargetExisting,
		UserID: &owner.ID, Actor: owner.Username,
	}
	if err := store.UpsertDevelopmentSession(params); err != nil {
		t.Fatal(err)
	}
	dep, err := store.BeginDeploymentWithOrigin(app.ID, "100", "/b/100", "", nil, db.DeploymentOrigin{
		Kind: db.DeploymentOriginDirect, Channel: db.DeploymentChannelCLI,
		DevelopmentSessionID: id, UserID: &owner.ID, Actor: owner.Username,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteDeployment(dep.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListDeploymentsBySlug(app.Slug)
	if err != nil || len(rows) != 1 {
		t.Fatalf("deployments = %+v, err=%v", rows, err)
	}
	got := rows[0].Provenance.DevelopmentSession
	if got == nil || got.ID != id || got.TargetKind != db.DevelopmentTargetExisting || rows[0].Provenance.Origin.DevelopmentSessionID != id {
		t.Fatalf("development provenance = %+v", rows[0].Provenance)
	}
	if err := store.EndDevelopmentSession(app.ID, id, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDevelopmentSession(params); err == nil {
		t.Fatal("ended session was reopened")
	}
	if err := store.EndDevelopmentSession(app.ID, "missing", time.Now()); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("missing end error = %v", err)
	}
}

func TestExpiredEphemeralAppsAreIsolatedFromPersistentApps(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "developer")
	temporary := mustCreateApp(t, store, "temporary", owner.ID)
	_ = mustCreateApp(t, store, "persistent", owner.ID)
	id := "abcdef0123456789abcdef0123456789"
	expires := time.Now().UTC().Add(-time.Minute)
	if err := store.UpsertDevelopmentSession(db.UpsertDevelopmentSessionParams{ID: id, AppID: temporary.ID, TargetKind: db.DevelopmentTargetEphemeral, ExpiresAt: &expires}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkEphemeralApp(temporary.ID, id, expires); err != nil {
		t.Fatal(err)
	}
	apps, err := store.ListExpiredEphemeralApps(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 || apps[0].Slug != "temporary" {
		t.Fatalf("expired apps = %+v", apps)
	}
}

func TestDevelopmentSessionLeaseRenewsAndClosesStaleSessions(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "lease-owner", "developer")
	app := mustCreateApp(t, store, "leased-app", owner.ID)
	id := "11111111111111111111111111111111"
	if err := store.UpsertDevelopmentSession(db.UpsertDevelopmentSessionParams{
		ID: id, AppID: app.ID, TargetKind: db.DevelopmentTargetExisting,
	}); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetDevelopmentSession(app.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // SQLite timestamps have one-second precision.
	if err := store.HeartbeatDevelopmentSession(db.UpsertDevelopmentSessionParams{
		ID: id, AppID: app.ID, TargetKind: db.DevelopmentTargetExisting,
	}); err != nil {
		t.Fatal(err)
	}
	after, err := store.GetDevelopmentSession(app.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) || !after.HeartbeatAt.After(before.HeartbeatAt) {
		t.Fatalf("heartbeat changed save time or failed to renew lease: before=%+v after=%+v", before, after)
	}
	if ended, err := store.EndStaleDevelopmentSessions(time.Hour); err != nil || ended != 0 {
		t.Fatalf("fresh lease ended=%d, err=%v", ended, err)
	}
	if ended, err := store.EndStaleDevelopmentSessions(0); err != nil || ended != 1 {
		t.Fatalf("stale lease ended=%d, err=%v", ended, err)
	}
	session, err := store.GetDevelopmentSession(app.ID, id)
	if err != nil || session.Status != db.DevelopmentSessionEnded || session.EndedAt == nil {
		t.Fatalf("ended session = %+v, err=%v", session, err)
	}
	if err := store.HeartbeatDevelopmentSession(db.UpsertDevelopmentSessionParams{
		ID: id, AppID: app.ID, TargetKind: db.DevelopmentTargetExisting,
	}); !errors.Is(err, db.ErrDevelopmentSessionConflict) {
		t.Fatalf("renew ended session error = %v", err)
	}
}

func TestCreateDevelopmentAppRollsBackEveryWriteOnSessionConflict(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "atomic-owner", "developer")
	id := "22222222222222222222222222222222"
	first := db.CreateDevelopmentAppParams{
		App:     db.CreateAppParams{Slug: "first-dev", Name: "First", OwnerID: owner.ID, Access: "private"},
		Session: db.UpsertDevelopmentSessionParams{ID: id, TargetKind: db.DevelopmentTargetCreated, UserID: &owner.ID},
	}
	if _, err := store.CreateDevelopmentApp(first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.App.Slug = "rolled-back-dev"
	second.App.Name = "Rolled back"
	second.App.ProjectSlug = "must-not-leak"
	if _, err := store.CreateDevelopmentApp(second); !errors.Is(err, db.ErrDevelopmentSessionConflict) {
		t.Fatalf("session conflict error = %v", err)
	}
	if _, err := store.GetAppBySlug(second.App.Slug); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("app survived rolled-back creation: %v", err)
	}
	if _, err := store.GetProject(second.App.ProjectSlug); !errors.Is(err, db.ErrProjectNotFound) {
		t.Fatalf("project survived rolled-back creation: %v", err)
	}
}

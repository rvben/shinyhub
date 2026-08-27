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

package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func TestListCrashedAppsWithLostReplicas(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "admin")

	seedReplica := func(t *testing.T, appID int64, status string) {
		t.Helper()
		if err := store.UpsertReplica(db.UpsertReplicaParams{
			AppID: appID, Index: 0, Status: status,
			Provider: "remote_docker", Tier: "remote", WorkerID: "node-a",
		}); err != nil {
			t.Fatalf("seed replica for app %d: %v", appID, err)
		}
	}

	// Crashed app with a lost replica: the one case that must be listed.
	stranded := mustCreateApp(t, store, "stranded", owner.ID)
	if err := store.MarkAppCrashed("stranded", "restart budget exhausted"); err != nil {
		t.Fatalf("mark stranded crashed: %v", err)
	}
	seedReplica(t, stranded.ID, db.ReplicaStatusLost)

	// Crashed app whose replicas are merely crashed: a genuine app fault, not a
	// worker loss; reviving it would flap a crash-looping app.
	appFault := mustCreateApp(t, store, "app-fault", owner.ID)
	if err := store.MarkAppCrashed("app-fault", "boot failure"); err != nil {
		t.Fatalf("mark app-fault crashed: %v", err)
	}
	seedReplica(t, appFault.ID, db.ReplicaStatusCrashed)

	// Running app with a lost replica: already reconcilable, nothing to revive.
	healing := mustCreateApp(t, store, "healing", owner.ID)
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "healing", Status: "running"}); err != nil {
		t.Fatalf("mark healing running: %v", err)
	}
	seedReplica(t, healing.ID, db.ReplicaStatusLost)

	apps, err := store.ListCrashedAppsWithLostReplicas()
	if err != nil {
		t.Fatalf("list crashed apps with lost replicas: %v", err)
	}
	if len(apps) != 1 || apps[0].Slug != "stranded" {
		slugs := make([]string, len(apps))
		for i, a := range apps {
			slugs[i] = a.Slug
		}
		t.Fatalf("listed apps = %v, want exactly [stranded]", slugs)
	}
}

func TestReviveCrashedApp(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "admin")
	mustCreateApp(t, store, "app", owner.ID)
	if err := store.MarkAppCrashed("app", "restart budget exhausted"); err != nil {
		t.Fatalf("mark crashed: %v", err)
	}

	revived, err := store.ReviveCrashedApp("app")
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	if !revived {
		t.Fatal("crashed app was not revived")
	}
	app, err := store.GetAppBySlug("app")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.Status != "degraded" {
		t.Fatalf("status = %q, want degraded", app.Status)
	}
	if app.LastError != "" || app.CrashedAt != 0 {
		t.Fatalf("crash diagnostic not cleared: last_error=%q crashed_at=%d", app.LastError, app.CrashedAt)
	}

	// CAS: a second revive (the app is no longer crashed) is a no-op.
	revived, err = store.ReviveCrashedApp("app")
	if err != nil {
		t.Fatalf("second revive: %v", err)
	}
	if revived {
		t.Fatal("revive reported a transition on a non-crashed app")
	}
	app, err = store.GetAppBySlug("app")
	if err != nil {
		t.Fatalf("get app after second revive: %v", err)
	}
	if app.Status != "degraded" {
		t.Fatalf("status after no-op revive = %q, want degraded (untouched)", app.Status)
	}
}

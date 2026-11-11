package db_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func TestAvailabilityFleetMembershipAndIdentity(t *testing.T) {
	s := openTestStore(t)
	u := mustCreateUser(t, s, "owner", "developer")
	for _, slug := range []string{"secret", "central", "opted"} {
		mustCreateApp(t, s, slug, u.ID)
	}
	app, err := s.GetAppBySlug("opted")
	if err != nil {
		t.Fatal(err)
	}
	set := func(mode string) {
		t.Helper()
		if _, err := s.PatchAppSettings(db.PatchAppSettingsParams{Slug: app.Slug, SetAvailability: true, Availability: mode}); err != nil {
			t.Fatal(err)
		}
	}
	set("public")
	for _, tc := range []struct {
		allow   bool
		central []string
		want    []string
	}{
		{false, nil, []string{}}, {true, nil, []string{"opted"}},
		{false, []string{"central", "missing"}, []string{"central"}},
		{true, []string{"opted", "central", "central"}, []string{"central", "opted"}},
	} {
		apps, err := s.ListAppAvailability(context.Background(), tc.central, tc.allow, false)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, 0)
		for _, a := range apps {
			got = append(got, a.Slug)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("allow=%v list=%v: got %v want %v", tc.allow, tc.central, got, tc.want)
		}
	}
	// An omitted manifest setting preserves opt-in; off explicitly revokes it.
	if _, err := s.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{AppID: app.ID, Slug: app.Slug}); err != nil {
		t.Fatal(err)
	}
	a, err := s.GetAppAvailability(context.Background(), app.Slug)
	if err != nil || a.Availability != "public" {
		t.Fatalf("omission reset opt-in: %+v %v", a, err)
	}
	set("off")
	apps, err := s.ListAppAvailability(context.Background(), nil, true, false)
	if err != nil || len(apps) != 0 {
		t.Fatalf("revocation: %+v %v", apps, err)
	}
	set("public")
	if err := s.DeleteApp(app.Slug); err != nil {
		t.Fatal(err)
	}
	mustCreateApp(t, s, app.Slug, u.ID)
	a, err = s.GetAppAvailability(context.Background(), app.Slug)
	if err != nil || a.Availability != "inherit" || a.OperatorStopped {
		t.Fatalf("reused slug inherited exposure/intent: %+v %v", a, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ListAppAvailability(ctx, []string{"central"}, true, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestAvailabilityOperatorStopIntent(t *testing.T) {
	s := openTestStore(t)
	u := mustCreateUser(t, s, "owner", "developer")
	app := mustCreateApp(t, s, "demo", u.ID)
	check := func(want bool) {
		t.Helper()
		a, err := s.GetAppAvailability(context.Background(), app.Slug)
		if err != nil || a.OperatorStopped != want {
			t.Fatalf("intent=%v: %+v %v", want, a, err)
		}
	}
	check(false)
	// A failed first boot or ordinary stopped write must not invent operator intent.
	if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "stopped", LastError: "boot failed"}); err != nil {
		t.Fatal(err)
	}
	check(false)
	if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "stopped", OperatorStopped: true}); err != nil {
		t.Fatal(err)
	}
	check(true)
	if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "stopped"}); err != nil {
		t.Fatal(err)
	}
	check(true)
	if err := s.ClearAppOperatorStopIntent(app.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "stopped", LastError: "restart failed"}); err != nil {
		t.Fatal(err)
	}
	check(false)
	if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "stopped", OperatorStopped: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "stopped"}); err != nil {
		t.Fatal(err)
	}
	check(false)
	if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "running", OperatorStopped: true}); err == nil {
		t.Fatal("accepted stop intent for running app")
	}
}

func TestAvailabilityFleetNeverSilentlyTruncates(t *testing.T) {
	s := openTestStore(t)
	u := mustCreateUser(t, s, "owner", "developer")
	_, err := s.DB().Exec(`WITH RECURSIVE n(i) AS (
 SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i < ?
) INSERT INTO apps (slug, name, owner_id, availability)
 SELECT 'app-' || CAST(i AS TEXT), 'Demo', ?, 'public' FROM n`, db.MaxAvailabilityApps, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	apps, err := s.ListAppAvailability(context.Background(), nil, true, false)
	if !errors.Is(err, db.ErrAvailabilityFleetTooLarge) || apps != nil {
		t.Fatalf("oversized snapshot returned partial success: len=%d err=%v", len(apps), err)
	}
	if _, err := s.DB().Exec(`DELETE FROM apps WHERE slug = ?`, "app-10000"); err != nil {
		t.Fatal(err)
	}
	apps, err = s.ListAppAvailability(context.Background(), nil, true, false)
	if err != nil || len(apps) != db.MaxAvailabilityApps {
		t.Fatalf("exact bound rejected: len=%d err=%v", len(apps), err)
	}
}

func TestAvailabilityHandoffPublishesAndCompensatesExposure(t *testing.T) {
	s := openTestStore(t)
	u := mustCreateUser(t, s, "owner", "developer")
	app := mustCreateApp(t, s, "demo", u.ID)
	old, err := s.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "old", BundleDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.BeginDeployment(app.ID, "next", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	after := *app
	after.Availability = "public"
	if err := s.StageHandoffSettings(next.ID, app, &after, nil, nil); err != nil {
		t.Fatal(err)
	}
	check := func(want string) {
		t.Helper()
		a, err := s.GetAppAvailability(context.Background(), app.Slug)
		if err != nil || a.Availability != want {
			t.Fatalf("want %s: %+v %v", want, a, err)
		}
	}
	check("inherit")
	if err := s.PromoteDeployment(next.ID); err != nil {
		t.Fatal(err)
	}
	check("public")
	if err := s.RevertDeploymentActivation(next.ID, old.ID, "cutover failed"); err != nil {
		t.Fatal(err)
	}
	check("inherit")
}

// A rolling-upgrade predecessor knows only status. The additive migration must
// not turn its ordinary status writes into constraint failures.
func TestAvailabilityMigrationAllowsLegacyStatusWrites(t *testing.T) {
	store := openTestStore(t)
	owner := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", owner.ID)
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "stopped", OperatorStopped: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE apps SET status = 'running' WHERE id = ?`, app.ID); err != nil {
		t.Fatalf("migration broke a predecessor status write: %v", err)
	}
	// New writers discard stale intent when the lifecycle next changes.
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "stopped"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetAppAvailability(context.Background(), app.Slug)
	if err != nil || got.OperatorStopped {
		t.Fatalf("stale legacy intent survived a new transition: %+v %v", got, err)
	}
}

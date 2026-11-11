package db_test

import (
	"reflect"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// withoutDeploymentSummary clears the fields only a full app read fills, so a
// full row and a lean row can be compared on everything they share.
func withoutDeploymentSummary(app *db.App) db.App {
	plain := *app
	plain.LastDeployedAt = nil
	plain.ReleaseNumber = 0
	plain.ReleasedAt = nil
	plain.CurrentVersion = ""
	plain.ContentDigest = ""
	plain.LastDeploymentStatus = ""
	return plain
}

func mustExec(t *testing.T, s *db.Store, query string, args ...any) {
	t.Helper()
	if _, err := s.DB().Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func mustSucceed(t *testing.T, s *db.Store, appID int64, version string) *db.Deployment {
	t.Helper()
	dep, err := s.BeginDeployment(appID, version, "/b/"+version)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PromoteDeployment(dep.ID); err != nil {
		t.Fatal(err)
	}
	return dep
}

func mustFail(t *testing.T, s *db.Store, appID int64, version string) *db.Deployment {
	t.Helper()
	dep, err := s.BeginDeployment(appID, version, "/b/"+version)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FailDeployment(dep.ID); err != nil {
		t.Fatal(err)
	}
	return dep
}

// The access lookup and the full lookup run side by side on every proxied
// request, so each must keep its own statement and agree on the plain columns.
func TestGetAppForAccessMatchesFullLookupOnPlainColumns(t *testing.T) {
	s := mustOpenDB(t)
	owner := mustCreateUser(t, s, "access-owner", "developer")
	app := mustCreateApp(t, s, "access", owner.ID)
	mustSucceed(t, s, app.ID, "v1")

	for round := 0; round < 2; round++ {
		full, err := s.GetAppBySlug("access")
		if err != nil {
			t.Fatalf("round %d: full lookup: %v", round, err)
		}
		lean, err := s.GetAppForAccess("access")
		if err != nil {
			t.Fatalf("round %d: access lookup: %v", round, err)
		}
		if full.ReleaseNumber != 1 || full.LastDeploymentStatus != "succeeded" {
			t.Fatalf("round %d: full lookup lost its deployment summary: release=%d status=%q",
				round, full.ReleaseNumber, full.LastDeploymentStatus)
		}
		if lean.OwnerID != owner.ID || lean.Access != "private" {
			t.Fatalf("round %d: access lookup owner=%d access=%q", round, lean.OwnerID, lean.Access)
		}
		if want := withoutDeploymentSummary(full); !reflect.DeepEqual(*lean, want) {
			t.Fatalf("round %d: access lookup differs on plain columns\n got %+v\nwant %+v", round, *lean, want)
		}
	}
	if _, err := s.GetAppForAccess("missing"); err != db.ErrNotFound {
		t.Fatalf("missing slug: err=%v, want ErrNotFound", err)
	}
}

// The watcher's per-tick lists keep the one deployment fact the lifecycle
// reads and leave the release summary unset.
func TestLifecycleListsCarryOnlyLastDeploymentStatus(t *testing.T) {
	s := mustOpenDB(t)
	owner := mustCreateUser(t, s, "tick-owner", "developer")
	running := mustCreateApp(t, s, "tick-running", owner.ID)
	waking := mustCreateApp(t, s, "tick-waking", owner.ID)
	mustSucceed(t, s, running.ID, "v1")
	mustFail(t, s, running.ID, "v2")
	mustSucceed(t, s, waking.ID, "v1")
	mustExec(t, s, "UPDATE apps SET status = 'running' WHERE id = ?", running.ID)
	mustExec(t, s, "UPDATE apps SET status = 'waking' WHERE id = ?", waking.ID)

	check := func(name, slug, wantStatus string, list []*db.App) {
		t.Helper()
		if len(list) != 1 || list[0].Slug != slug {
			t.Fatalf("%s: got %d apps, want only %q", name, len(list), slug)
		}
		full, err := s.GetAppBySlug(slug)
		if err != nil {
			t.Fatal(err)
		}
		got := list[0]
		if got.LastDeploymentStatus != wantStatus || got.LastDeploymentStatus != full.LastDeploymentStatus {
			t.Fatalf("%s: last deployment status=%q, full lookup=%q, want %q",
				name, got.LastDeploymentStatus, full.LastDeploymentStatus, wantStatus)
		}
		if got.ReleaseNumber != 0 || got.LastDeployedAt != nil || got.ReleasedAt != nil ||
			got.CurrentVersion != "" || got.ContentDigest != "" {
			t.Fatalf("%s: release summary populated: %+v", name, got)
		}
		gotPlain, fullPlain := withoutDeploymentSummary(got), withoutDeploymentSummary(full)
		if !reflect.DeepEqual(gotPlain, fullPlain) {
			t.Fatalf("%s: plain columns differ\n got %+v\nwant %+v", name, gotPlain, fullPlain)
		}
	}
	reconcilable, err := s.ListReconcilableApps()
	if err != nil {
		t.Fatal(err)
	}
	check("reconcilable", "tick-running", "failed", reconcilable)
	wakingApps, err := s.ListWakingApps()
	if err != nil {
		t.Fatal(err)
	}
	check("waking", "tick-waking", "succeeded", wakingApps)
}

// ServingWorkerIsolation answers in one statement what
// ResolveServingWorkerIsolation derives from two reads. Every state a
// deployment history can be in must give the same answer both ways.
func TestServingWorkerIsolationMatchesTwoReadResolver(t *testing.T) {
	s := mustOpenDB(t)
	owner := mustCreateUser(t, s, "parity-owner", "developer")
	app := mustCreateApp(t, s, "parity", owner.ID)
	other := mustCreateApp(t, s, "parity-other", owner.ID)

	compare := func(step, want string) {
		t.Helper()
		for _, desired := range []string{"", "grouped", "per_session"} {
			for _, def := range []string{"", "multiplex", "grouped"} {
				probe := *app
				probe.WorkerIsolation = desired
				got, gotErr := s.ServingWorkerIsolation(&probe, def)
				oracle, oracleErr := db.ResolveServingWorkerIsolation(s, &probe, def)
				if got != oracle || (gotErr == nil) != (oracleErr == nil) {
					t.Fatalf("%s (desired=%q default=%q): got %q/%v, two-read resolver %q/%v",
						step, desired, def, got, gotErr, oracle, oracleErr)
				}
			}
		}
		probe := *app
		probe.WorkerIsolation = "grouped"
		if got, err := s.ServingWorkerIsolation(&probe, "multiplex"); err != nil || got != want {
			t.Fatalf("%s: serving isolation=%q %v, want %q", step, got, err, want)
		}
	}
	record := func(dep *db.Deployment, isolation string) {
		t.Helper()
		if err := s.RecordDeploymentWorkerIsolation(app.ID, dep.ID, isolation); err != nil {
			t.Fatal(err)
		}
	}
	begin := func(version string) *db.Deployment {
		t.Helper()
		dep, err := s.BeginDeployment(app.ID, version, "/b/"+version)
		if err != nil {
			t.Fatal(err)
		}
		return dep
	}

	compare("no deployment", "grouped")

	first := begin("v1")
	record(first, "per_session")
	compare("pending candidate only", "grouped")

	if err := s.PromoteDeployment(first.ID); err != nil {
		t.Fatal(err)
	}
	compare("one succeeded release", "per_session")

	second := begin("v2")
	record(second, "multiplex")
	compare("succeeded release with a pending candidate", "per_session")

	if err := s.FailDeployment(second.ID); err != nil {
		t.Fatal(err)
	}
	compare("newest attempt failed", "per_session")

	legacy := begin("v3")
	if err := s.PromoteDeployment(legacy.ID); err != nil {
		t.Fatal(err)
	}
	compare("serving release recorded no isolation", "grouped")

	// The active generation wins over a newer succeeded release.
	mustExec(t, s, "UPDATE apps SET active_deployment_id = ? WHERE id = ?", first.ID, app.ID)
	compare("active generation is an older release", "per_session")

	// A generation pointer at a failed deployment is not a serving one.
	mustExec(t, s, "UPDATE apps SET active_deployment_id = ? WHERE id = ?", second.ID, app.ID)
	compare("active generation failed", "grouped")

	// Nor is one that belongs to another app.
	foreign := mustSucceed(t, s, other.ID, "v1")
	if err := s.RecordDeploymentWorkerIsolation(other.ID, foreign.ID, "multiplex"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, "UPDATE apps SET active_deployment_id = ? WHERE id = ?", foreign.ID, app.ID)
	compare("active generation belongs to another app", "grouped")

	// With no generation pointer, a serving-only activation row is skipped in
	// favour of the newest real release.
	mustExec(t, s, "UPDATE apps SET active_deployment_id = NULL WHERE id = ?", app.ID)
	mustExec(t, s, "UPDATE deployments SET serving_activation_id = 1 WHERE id = ?", legacy.ID)
	compare("newest succeeded row is a serving activation", "per_session")
}

// SQL pagination must cut the same rows, with the same release numbers, that
// slicing the whole history gives.
func TestListDeploymentsBySlugPageMatchesWholeHistory(t *testing.T) {
	s := mustOpenDB(t)
	owner := mustCreateUser(t, s, "history-owner", "developer")
	app := mustCreateApp(t, s, "history", owner.ID)
	other := mustCreateApp(t, s, "history-other", owner.ID)

	v1 := mustSucceed(t, s, app.ID, "100")
	mustSucceed(t, s, other.ID, "900")
	mustFail(t, s, app.ID, "150")
	mustSucceed(t, s, app.ID, "200")
	activation := mustSucceed(t, s, app.ID, "200")
	if _, err := s.BeginDeployment(app.ID, "250", "/b/250"); err != nil {
		t.Fatal(err)
	}
	restore := mustSucceed(t, s, app.ID, "100")
	restoreOfActivation := mustSucceed(t, s, app.ID, "300")
	mustExec(t, s, "UPDATE deployments SET serving_activation_id = 1 WHERE id = ?", activation.ID)
	mustExec(t, s, "UPDATE deployments SET restored_from_id = ? WHERE id = ?", v1.ID, restore.ID)
	mustExec(t, s, "UPDATE deployments SET restored_from_id = ? WHERE id = ?", activation.ID, restoreOfActivation.ID)

	whole, err := s.ListDeploymentsBySlug("history")
	if err != nil {
		t.Fatal(err)
	}
	if len(whole) != 6 {
		t.Fatalf("history has %d rows, want 6 (the activation row is hidden)", len(whole))
	}

	// Each number is checked against a direct count, independent of the query
	// under test.
	count := func(uptoID int64) int64 {
		t.Helper()
		var n int64
		if err := s.DB().QueryRow(`SELECT COUNT(*) FROM deployments
			WHERE app_id = ? AND status = 'succeeded' AND serving_activation_id IS NULL AND id <= ?`,
			app.ID, uptoID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	numbered := 0
	for i, d := range whole {
		if i > 0 && d.ID >= whole[i-1].ID {
			t.Fatalf("row %d: id %d is not newest-first", i, d.ID)
		}
		if d.Status == "succeeded" {
			numbered++
			if d.ReleaseNumber == nil || *d.ReleaseNumber != count(d.ID) {
				t.Fatalf("deployment %d: release number %v, want %d", d.ID, d.ReleaseNumber, count(d.ID))
			}
		} else if d.ReleaseNumber != nil {
			t.Fatalf("deployment %d (%s) carries release number %d", d.ID, d.Status, *d.ReleaseNumber)
		}
		if d.RestoredFromDeploymentID != nil {
			want := count(*d.RestoredFromDeploymentID)
			if d.RestoredFromReleaseNumber == nil || *d.RestoredFromReleaseNumber != want {
				t.Fatalf("deployment %d: restored-from release %v, want %d", d.ID, d.RestoredFromReleaseNumber, want)
			}
		}
	}
	if numbered != 4 {
		t.Fatalf("%d numbered releases, want 4", numbered)
	}
	if got := *whole[0].ReleaseNumber; got != 4 {
		t.Fatalf("newest release is v%d, want v4", got)
	}
	if whole[1].RestoredFromReleaseNumber == nil || *whole[1].RestoredFromReleaseNumber != 1 {
		t.Fatalf("restore of v1 reports source %v", whole[1].RestoredFromReleaseNumber)
	}

	for _, tc := range []struct{ limit, offset int }{
		{0, 0}, {2, 0}, {2, 2}, {2, 4}, {4, 5}, {3, 6}, {3, 60}, {0, 3}, {100, 0},
	} {
		page, total, err := s.ListDeploymentsBySlugPage("history", tc.limit, tc.offset)
		if err != nil {
			t.Fatalf("limit=%d offset=%d: %v", tc.limit, tc.offset, err)
		}
		if total != len(whole) {
			t.Fatalf("limit=%d offset=%d: total=%d, want %d", tc.limit, tc.offset, total, len(whole))
		}
		start := min(tc.offset, len(whole))
		end := len(whole)
		if tc.limit > 0 {
			end = min(start+tc.limit, len(whole))
		}
		want := whole[start:end]
		if len(page) != len(want) {
			t.Fatalf("limit=%d offset=%d: %d rows, want %d", tc.limit, tc.offset, len(page), len(want))
		}
		if page == nil {
			t.Fatalf("limit=%d offset=%d: nil page, want an empty slice", tc.limit, tc.offset)
		}
		for i := range want {
			if !reflect.DeepEqual(page[i], want[i]) {
				t.Fatalf("limit=%d offset=%d row %d:\n got %+v\nwant %+v", tc.limit, tc.offset, i, page[i], want[i])
			}
		}
	}

	empty, total, err := s.ListDeploymentsBySlugPage("no-such-app", 5, 0)
	if err != nil || total != 0 || empty == nil || len(empty) != 0 {
		t.Fatalf("unknown slug: page=%v total=%d err=%v", empty, total, err)
	}
}

func TestListProjectDisplayMatchesProjectList(t *testing.T) {
	s := mustOpenDB(t)
	for _, p := range []db.Project{
		{Slug: "zeta", Name: "Zeta", IconEmoji: "Z"},
		{Slug: "alpha", Name: "Alpha", Description: "first"},
	} {
		if _, err := s.UpsertProject(p); err != nil {
			t.Fatal(err)
		}
	}
	full, err := s.ListProjects(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	display, err := s.ListProjectDisplay()
	if err != nil {
		t.Fatal(err)
	}
	if len(full) < 2 || len(display) != len(full) {
		t.Fatalf("display lists %d projects, full list %d", len(display), len(full))
	}
	for i, p := range full {
		if got := display[i]; got.Slug != p.Slug || got.Name != p.Name || got.IconEmoji != p.IconEmoji {
			t.Fatalf("project %d: display %+v, full %+v", i, got, *p)
		}
	}
}

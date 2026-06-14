package db_test

import "testing"

// TestReleaseNumber verifies the human-friendly per-app release number (v1, v2,
// …): ListDeploymentsBySlug ranks SUCCEEDED rows by id, skips failed/pending
// rows (nil), and a rollback (which inserts a NEW succeeded row reusing an old
// version) gets the next number. CurrentRelease returns the live release + date.
func TestReleaseNumber(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "app", owner.ID)

	succeed := func(version string) {
		d, err := store.BeginDeployment(app.ID, version, "/b/"+version)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PromoteDeployment(d.ID); err != nil {
			t.Fatal(err)
		}
	}
	fail := func(version string) {
		d, err := store.BeginDeployment(app.ID, version, "/b/"+version)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.FailDeployment(d.ID); err != nil {
			t.Fatal(err)
		}
	}

	// No succeeded deploy yet → no current release.
	if _, _, _, ok := store.CurrentRelease(app.ID); ok {
		t.Fatal("CurrentRelease ok=true with zero deploys, want false")
	}

	succeed("100") // id1 → v1
	fail("150")    // id2 → failed attempt, no number
	succeed("200") // id3 → v2
	succeed("100") // id4 → rollback to the v1 bundle, a NEW succeeded row → v3

	rows, err := store.ListDeploymentsBySlug("app")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("got %d deployment rows, want 4", len(rows))
	}
	// Newest-first by id: rollback(v3), v2, failed(nil), v1.
	norm := func(p *int64) int64 {
		if p == nil {
			return -1
		}
		return *p
	}
	want := []int64{3, 2, -1, 1}
	for i, d := range rows {
		if norm(d.ReleaseNumber) != want[i] {
			t.Errorf("row %d (version=%s status=%s): release_number=%d, want %d",
				i, d.Version, d.Status, norm(d.ReleaseNumber), want[i])
		}
	}

	// CurrentRelease = 3 succeeded; the live row is the newest succeeded (the
	// rollback), so its version is the reused "100" bundle.
	n, at, version, ok := store.CurrentRelease(app.ID)
	if !ok {
		t.Fatal("CurrentRelease ok=false after deploys, want true")
	}
	if n != 3 {
		t.Errorf("CurrentRelease number = %d, want 3", n)
	}
	if at.IsZero() {
		t.Error("CurrentRelease releasedAt is zero, want the live deploy timestamp")
	}
	if version != "100" {
		t.Errorf("CurrentRelease version = %q, want \"100\" (the rolled-back-to live bundle)", version)
	}
}

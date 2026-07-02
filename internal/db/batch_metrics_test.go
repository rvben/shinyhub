package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestGetAppsBySlugs verifies the batch app fetch returns exactly the known
// slugs (skipping unknown ones) in one query, so the metrics endpoint need not
// call GetAppBySlug per card.
func TestGetAppsBySlugs(t *testing.T) {
	s := openTestStore(t)
	owner := mustCreateUser(t, s, "owner", "admin")
	mustCreateApp(t, s, "alpha", owner.ID)
	mustCreateApp(t, s, "bravo", owner.ID)
	mustCreateApp(t, s, "charlie", owner.ID)

	got, err := s.GetAppsBySlugs([]string{"alpha", "charlie", "missing"})
	if err != nil {
		t.Fatalf("GetAppsBySlugs: %v", err)
	}
	bySlug := map[string]*db.App{}
	for _, a := range got {
		bySlug[a.Slug] = a
	}
	if len(bySlug) != 2 || bySlug["alpha"] == nil || bySlug["charlie"] == nil {
		t.Fatalf("want alpha+charlie, got %d: %v", len(got), keysOfApps(got))
	}
	if bySlug["missing"] != nil {
		t.Error("unknown slug must not be returned")
	}
	// Empty input is a no-op, not a malformed IN () query.
	if empty, err := s.GetAppsBySlugs(nil); err != nil || len(empty) != 0 {
		t.Errorf("GetAppsBySlugs(nil) = %v, %v; want empty, nil", empty, err)
	}
}

// TestListReplicasForApps verifies replicas for many apps come back in one
// query, grouped by app ID.
func TestListReplicasForApps(t *testing.T) {
	s := openTestStore(t)
	owner := mustCreateUser(t, s, "owner", "admin")
	a := mustCreateApp(t, s, "alpha", owner.ID)
	b := mustCreateApp(t, s, "bravo", owner.ID)
	for _, p := range []db.UpsertReplicaParams{
		{AppID: a.ID, Index: 0, Status: "running", Provider: "native", Tier: "default"},
		{AppID: a.ID, Index: 1, Status: "running", Provider: "native", Tier: "default"},
		{AppID: b.ID, Index: 0, Status: "lost", Provider: "native", Tier: "remote"},
	} {
		if err := s.UpsertReplica(p); err != nil {
			t.Fatalf("seed replica: %v", err)
		}
	}

	byApp, err := s.ListReplicasForApps([]int64{a.ID, b.ID})
	if err != nil {
		t.Fatalf("ListReplicasForApps: %v", err)
	}
	if len(byApp[a.ID]) != 2 {
		t.Errorf("app a: want 2 replicas, got %d", len(byApp[a.ID]))
	}
	if len(byApp[b.ID]) != 1 || byApp[b.ID][0].Status != "lost" {
		t.Errorf("app b: want 1 lost replica, got %v", byApp[b.ID])
	}
	if empty, err := s.ListReplicasForApps(nil); err != nil || len(empty) != 0 {
		t.Errorf("ListReplicasForApps(nil) = %v, %v; want empty, nil", empty, err)
	}
}

// TestLatestAutoscaleEventForSlugs verifies the batch returns the single latest
// autoscale event per slug (greatest-n-per-group), matching the per-slug
// LatestAutoscaleEvent.
func TestLatestAutoscaleEventForSlugs(t *testing.T) {
	s := openTestStore(t)
	owner := mustCreateUser(t, s, "owner", "admin")
	mustCreateApp(t, s, "alpha", owner.ID)
	mustCreateApp(t, s, "bravo", owner.ID)

	// Two events for alpha; the later one (scale_down) must win. One for bravo.
	logAutoscale(t, s, owner.ID, "alpha", "autoscale_scale_up", "1->2")
	logAutoscale(t, s, owner.ID, "alpha", "autoscale_scale_down", "2->1")
	logAutoscale(t, s, owner.ID, "bravo", "autoscale_scale_up", "1->3")

	got, err := s.LatestAutoscaleEventForSlugs([]string{"alpha", "bravo", "charlie"})
	if err != nil {
		t.Fatalf("LatestAutoscaleEventForSlugs: %v", err)
	}
	if ev, ok := got["alpha"]; !ok || ev.Action != "autoscale_scale_down" {
		t.Errorf("alpha latest = %+v, want autoscale_scale_down", got["alpha"])
	}
	if ev, ok := got["bravo"]; !ok || ev.Action != "autoscale_scale_up" {
		t.Errorf("bravo latest = %+v, want autoscale_scale_up", got["bravo"])
	}
	if _, ok := got["charlie"]; ok {
		t.Error("charlie has no autoscale events; must be absent from the map")
	}

	// Batch result must agree with the per-slug method.
	single, found, err := s.LatestAutoscaleEvent("alpha")
	if err != nil || !found {
		t.Fatalf("LatestAutoscaleEvent(alpha): %v found=%v", err, found)
	}
	if got["alpha"].ID != single.ID {
		t.Errorf("batch alpha id=%d, per-slug id=%d; must match", got["alpha"].ID, single.ID)
	}
}

func keysOfApps(apps []*db.App) []string {
	out := make([]string, 0, len(apps))
	for _, a := range apps {
		out = append(out, a.Slug)
	}
	return out
}

func logAutoscale(t *testing.T, s *db.Store, uid int64, slug, action, detail string) {
	t.Helper()
	s.LogAuditEvent(db.AuditEventParams{
		UserID: &uid, Action: action, ResourceType: "app", ResourceID: slug, Detail: detail,
	})
}

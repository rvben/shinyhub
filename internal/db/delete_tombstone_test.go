package db_test

import (
	"sort"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func TestAllSlugs_ReturnsEveryStatus(t *testing.T) {
	s := mustOpenDB(t)
	u := mustCreateUser(t, s, "owner", "admin")
	mustCreateApp(t, s, "alpha", u.ID)
	mustCreateApp(t, s, "beta", u.ID)
	if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "beta", Status: "deleting"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.AllSlugs()
	if err != nil {
		t.Fatalf("AllSlugs: %v", err)
	}
	sort.Strings(got)
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("AllSlugs = %v, want [alpha beta] (deleting included)", got)
	}
}

func TestListDeletingApps_OnlyTombstoned(t *testing.T) {
	s := mustOpenDB(t)
	u := mustCreateUser(t, s, "owner", "admin")
	mustCreateApp(t, s, "live", u.ID)
	mustCreateApp(t, s, "dying", u.ID)
	if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "dying", Status: "deleting"}); err != nil {
		t.Fatal(err)
	}

	apps, err := s.ListDeletingApps()
	if err != nil {
		t.Fatalf("ListDeletingApps: %v", err)
	}
	if len(apps) != 1 || apps[0].Slug != "dying" {
		t.Fatalf("ListDeletingApps = %+v, want only dying", apps)
	}
}

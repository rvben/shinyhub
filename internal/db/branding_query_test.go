package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func TestListPublicAppsExcludesSharedAndPrivate(t *testing.T) {
	s := mustOpenDB(t)
	owner := mustCreateUser(t, s, "branding-owner", "developer")

	for _, tc := range []struct {
		slug   string
		access string
	}{
		{"pub", "public"},
		{"shr", "shared"},
		{"prv", "private"},
	} {
		if err := s.CreateApp(db.CreateAppParams{
			Slug:    tc.slug,
			Name:    tc.slug,
			OwnerID: owner.ID,
			Access:  tc.access,
		}); err != nil {
			t.Fatalf("create app %q: %v", tc.slug, err)
		}
	}

	apps, err := s.ListPublicApps(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 || apps[0].Slug != "pub" {
		t.Fatalf("ListPublicApps must return only public apps, got %+v", apps)
	}
}

package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestListUsersExcludingServiceAccounts_ExcludesAndPaginatesCorrectly proves
// the admin users list paginates in SQL over the already-filtered row set,
// not by fetching a raw page and then dropping service accounts from it. The
// deploy service account's username ("__deploy__") sorts before every human
// username under the default byte-wise collation, so a raw SQL page starting
// at offset 0 always carries it - a Go-side filter applied AFTER paginating
// would silently trim that page below the requested limit. Filtering it out
// in the WHERE clause instead keeps every page full size.
func TestListUsersExcludingServiceAccounts_ExcludesAndPaginatesCorrectly(t *testing.T) {
	store := dbtest.New(t)

	if _, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer"); err != nil {
		t.Fatalf("create service account: %v", err)
	}
	seedUser(t, store, "alice", "developer")
	seedUser(t, store, "bob", "developer")
	seedUser(t, store, "carol", "developer")

	total, err := store.CountUsersExcludingServiceAccounts()
	if err != nil {
		t.Fatalf("CountUsersExcludingServiceAccounts: %v", err)
	}
	if total != 3 {
		t.Fatalf("CountUsersExcludingServiceAccounts = %d, want 3 (service account excluded)", total)
	}

	page1, err := store.ListUsersExcludingServiceAccounts(2, 0)
	if err != nil {
		t.Fatalf("ListUsersExcludingServiceAccounts(2, 0): %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page 1 = %d users, want 2 (a Go-side post-filter would have trimmed this to 1)", len(page1))
	}
	if page1[0].Username != "alice" || page1[1].Username != "bob" {
		t.Errorf("page 1 = [%s, %s], want [alice, bob]", page1[0].Username, page1[1].Username)
	}

	page2, err := store.ListUsersExcludingServiceAccounts(2, 2)
	if err != nil {
		t.Fatalf("ListUsersExcludingServiceAccounts(2, 2): %v", err)
	}
	if len(page2) != 1 {
		t.Fatalf("page 2 = %d users, want 1", len(page2))
	}
	if page2[0].Username != "carol" {
		t.Errorf("page 2 = [%s], want [carol]", page2[0].Username)
	}

	for _, page := range [][]*db.User{page1, page2} {
		for _, u := range page {
			if u.PrincipalType == "service_account" {
				t.Errorf("service account %q leaked into a filtered page", u.Username)
			}
		}
	}

	// The unfiltered, general-purpose ListUsers must still see the service
	// account: callers like the startup admin bootstrap scan (cmd/shinyhub)
	// filter it themselves and rely on ListUsers carrying every principal type.
	all, err := store.ListUsers(0, 0)
	if err != nil {
		t.Fatalf("ListUsers(0, 0): %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("ListUsers(0, 0) = %d users, want 4 (3 human + 1 service account)", len(all))
	}
}

package db_test

import (
	"errors"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func seedUser(t *testing.T, store *db.Store, name, role string) int64 {
	t.Helper()
	if err := store.CreateUser(db.CreateUserParams{Username: name, PasswordHash: "h", Role: role}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByUsername(name)
	return u.ID
}

func TestMigration028_UserGroupsRoundTrip(t *testing.T) {
	store := dbtest.New(t)
	id := seedUser(t, store, "u1", "viewer")
	if err := store.ReplaceUserGroups(id, []string{"g1", "g2"}); err != nil {
		t.Fatalf("ReplaceUserGroups: %v", err)
	}
	groups, err := store.GetUserGroups(id)
	if err != nil {
		t.Fatalf("GetUserGroups: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("groups = %v, want 2", groups)
	}
}

func TestReconcile_GroupDerivedRole(t *testing.T) {
	store := dbtest.New(t)
	id := seedUser(t, store, "alice", "viewer")
	maps := []auth.GroupRoleMapping{{Group: "devs", Role: "developer"}}
	if err := store.ReconcileUserFromGroups(id, []string{"devs"}, maps, "viewer"); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByID(id)
	if u.Role != "developer" {
		t.Fatalf("role = %q, want developer", u.Role)
	}
}

func TestReconcile_AuthoritativeDemotionOnGroupRemoval(t *testing.T) {
	store := dbtest.New(t)
	id := seedUser(t, store, "bob", "viewer")
	maps := []auth.GroupRoleMapping{{Group: "devs", Role: "developer"}}
	_ = store.ReconcileUserFromGroups(id, []string{"devs"}, maps, "viewer")
	if err := store.ReconcileUserFromGroups(id, []string{"other"}, maps, "viewer"); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByID(id)
	if u.Role != "viewer" {
		t.Fatalf("role = %q, want viewer (authoritative demotion)", u.Role)
	}
}

func TestReconcile_ManualOverrideWins(t *testing.T) {
	store := dbtest.New(t)
	id := seedUser(t, store, "carol", "viewer")
	if err := store.SetManualRole(id, "operator"); err != nil {
		t.Fatal(err)
	}
	maps := []auth.GroupRoleMapping{{Group: "devs", Role: "developer"}}
	if err := store.ReconcileUserFromGroups(id, []string{"devs"}, maps, "viewer"); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByID(id)
	if u.Role != "operator" {
		t.Fatalf("role = %q, want operator (manual override wins)", u.Role)
	}
}

func TestClearManualRole_ReturnsToGroupGovernance(t *testing.T) {
	store := dbtest.New(t)
	// Seed a second admin so clearing dan's override does not strand the system.
	seedUser(t, store, "other-admin", "admin")
	id := seedUser(t, store, "dan", "viewer")
	store.SetManualRole(id, "admin")
	store.ReplaceUserGroups(id, []string{"devs"})
	maps := []auth.GroupRoleMapping{{Group: "devs", Role: "developer"}}
	if err := store.ClearManualRole(id, maps, "viewer"); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByID(id)
	if u.Role != "developer" {
		t.Fatalf("role = %q, want developer after clearing override", u.Role)
	}
}

func TestReconcile_NeverDemotesLastAdmin(t *testing.T) {
	store := dbtest.New(t)
	id := seedUser(t, store, "root", "admin")
	store.SetManualRole(id, "admin")
	// Automatic reconcile must keep admin (no error), not demote.
	if err := store.ReconcileUserFromGroups(id, []string{"nope"}, nil, "viewer"); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByID(id)
	if u.Role != "admin" {
		t.Fatalf("reconcile demoted the last admin to %q", u.Role)
	}
}

func TestSetManualRole_LastAdminRejected(t *testing.T) {
	store := dbtest.New(t)
	id := seedUser(t, store, "root", "admin")
	store.SetManualRole(id, "admin")
	if err := store.SetManualRole(id, "viewer"); !errors.Is(err, db.ErrLastAdmin) {
		t.Fatalf("SetManualRole demoting last admin = %v, want ErrLastAdmin", err)
	}
}

func TestClearManualRole_LastAdminRejected(t *testing.T) {
	store := dbtest.New(t)
	id := seedUser(t, store, "root", "admin")
	store.SetManualRole(id, "admin")
	// No groups -> clearing would drop to default (viewer) and strand the admin.
	if err := store.ClearManualRole(id, nil, "viewer"); !errors.Is(err, db.ErrLastAdmin) {
		t.Fatalf("ClearManualRole on last admin = %v, want ErrLastAdmin", err)
	}
	u, _ := store.GetUserByID(id)
	if u.Role != "admin" {
		t.Fatalf("last admin demoted to %q despite guard", u.Role)
	}
}

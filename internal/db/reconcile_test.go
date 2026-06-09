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

func TestDeleteUser_LastAdminRejected(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "root", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	root, _ := store.GetUserByUsername("root")
	// Deleting the only admin must be refused, not silently leave zero admins.
	if err := store.DeleteUser(root.ID); !errors.Is(err, db.ErrLastAdmin) {
		t.Fatalf("DeleteUser on last admin = %v, want ErrLastAdmin", err)
	}
	// The admin is still present.
	if _, err := store.GetUserByID(root.ID); err != nil {
		t.Fatalf("last admin was deleted despite guard: %v", err)
	}
}

func TestDeleteUser_NonLastAdminAllowed(t *testing.T) {
	store := dbtest.New(t)
	store.CreateUser(db.CreateUserParams{Username: "a", PasswordHash: "h", Role: "admin"})
	store.CreateUser(db.CreateUserParams{Username: "b", PasswordHash: "h", Role: "admin"})
	b, _ := store.GetUserByUsername("b")
	if err := store.DeleteUser(b.ID); err != nil {
		t.Fatalf("deleting a non-last admin should succeed: %v", err)
	}
}

func TestDeleteUser_NonAdminAllowed(t *testing.T) {
	store := dbtest.New(t)
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: "h", Role: "admin"})
	store.CreateUser(db.CreateUserParams{Username: "dev", PasswordHash: "h", Role: "developer"})
	dev, _ := store.GetUserByUsername("dev")
	if err := store.DeleteUser(dev.ID); err != nil {
		t.Fatalf("deleting a non-admin should succeed: %v", err)
	}
}

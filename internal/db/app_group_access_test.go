package db_test

import (
	"errors"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func agaOwner(t *testing.T, store *db.Store) int64 {
	t.Helper()
	_ = store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	return owner.ID
}

func TestAppGroupAccess_GrantListRevoke(t *testing.T) {
	store := dbtest.New(t)
	ownerID := agaOwner(t, store)
	if err := store.CreateApp(db.CreateAppParams{Slug: "finance-dash", Name: "fd", OwnerID: ownerID}); err != nil {
		t.Fatal(err)
	}
	if err := store.GrantAppGroupAccess("finance-dash", "finance", "viewer", "manual"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := store.GrantAppGroupAccess("finance-dash", "finance-leads", "manager", "manual"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	rules, err := store.ListAppGroupAccess("finance-dash")
	if err != nil || len(rules) != 2 {
		t.Fatalf("list rules=%v err=%v, want 2", rules, err)
	}
	// Re-grant upserts the role.
	if err := store.GrantAppGroupAccess("finance-dash", "finance", "manager", "manual"); err != nil {
		t.Fatalf("re-grant: %v", err)
	}
	rules, _ = store.ListAppGroupAccess("finance-dash")
	var financeRole string
	for _, r := range rules {
		if r.Group == "finance" {
			financeRole = r.Role
		}
	}
	if financeRole != "manager" {
		t.Fatalf("finance role = %q, want manager after re-grant", financeRole)
	}
	if err := store.RevokeAppGroupAccess("finance-dash", "finance"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := store.RevokeAppGroupAccess("finance-dash", "nope"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("revoke missing = %v, want ErrNotFound", err)
	}
}

func TestGroupRoleForUserOnApp(t *testing.T) {
	store := dbtest.New(t)
	ownerID := agaOwner(t, store)
	store.CreateApp(db.CreateAppParams{Slug: "app1", Name: "a1", OwnerID: ownerID})
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: "h", Role: "viewer"})
	alice, _ := store.GetUserByUsername("alice")
	store.ReplaceUserGroups(alice.ID, []string{"finance", "finance-leads"})
	store.GrantAppGroupAccess("app1", "finance", "viewer", "manual")
	store.GrantAppGroupAccess("app1", "finance-leads", "manager", "manual")
	role, ok, err := store.GroupRoleForUserOnApp("app1", alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || role != "manager" {
		t.Fatalf("got (%q,%v), want (manager,true)", role, ok)
	}
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: "h", Role: "viewer"})
	bob, _ := store.GetUserByUsername("bob")
	store.ReplaceUserGroups(bob.ID, []string{"other"})
	if _, ok, err := store.GroupRoleForUserOnApp("app1", bob.ID); err != nil || ok {
		t.Fatal("bob has no matching group; want ok=false")
	}
}

func TestUserCanAccessApp_ViaGroup(t *testing.T) {
	store := dbtest.New(t)
	ownerID := agaOwner(t, store)
	store.CreateApp(db.CreateAppParams{Slug: "priv", Name: "p", OwnerID: ownerID})
	store.SetAppAccess("priv", "private")
	store.CreateUser(db.CreateUserParams{Username: "carol", PasswordHash: "h", Role: "viewer"})
	carol, _ := store.GetUserByUsername("carol")
	store.ReplaceUserGroups(carol.ID, []string{"finance"})
	store.GrantAppGroupAccess("priv", "finance", "viewer", "manual")
	ok, err := store.UserCanAccessApp("priv", carol.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("group grant should make UserCanAccessApp true")
	}
}

func TestListAppsVisibleToUser_ViaGroup(t *testing.T) {
	store := dbtest.New(t)
	ownerID := agaOwner(t, store)
	store.CreateApp(db.CreateAppParams{Slug: "priv-list", Name: "pl", OwnerID: ownerID})
	store.SetAppAccess("priv-list", "private")
	store.GrantAppGroupAccess("priv-list", "finance", "viewer", "manual")

	// A user in the granted group sees the private app in their list.
	store.CreateUser(db.CreateUserParams{Username: "ingroup", PasswordHash: "h", Role: "viewer"})
	ingroup, _ := store.GetUserByUsername("ingroup")
	store.ReplaceUserGroups(ingroup.ID, []string{"finance"})
	apps, err := store.ListAppsVisibleToUser(ingroup.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !appsContainSlug(apps, "priv-list") {
		t.Fatal("user in the granted group should see the private app in their list")
	}

	// A user in a non-matching group does NOT see it.
	store.CreateUser(db.CreateUserParams{Username: "outgroup", PasswordHash: "h", Role: "viewer"})
	outgroup, _ := store.GetUserByUsername("outgroup")
	store.ReplaceUserGroups(outgroup.ID, []string{"other"})
	apps, err = store.ListAppsVisibleToUser(outgroup.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if appsContainSlug(apps, "priv-list") {
		t.Fatal("user with no matching group must not see the private app")
	}
}

func appsContainSlug(apps []*db.App, slug string) bool {
	for _, a := range apps {
		if a.Slug == slug {
			return true
		}
	}
	return false
}

func TestReconcileFromManifest_AddUpdateDelete(t *testing.T) {
	store := dbtest.New(t)
	ownerID := agaOwner(t, store)
	store.CreateApp(db.CreateAppParams{Slug: "m1", Name: "m1", OwnerID: ownerID})

	skipped, err := store.ReconcileAppGroupAccessFromManifest("m1", []db.AppGroupRule{
		{Group: "finance", Role: "viewer"},
		{Group: "leads", Role: "manager"},
	})
	if err != nil || len(skipped) != 0 {
		t.Fatalf("reconcile1: skipped=%v err=%v", skipped, err)
	}
	rules, _ := store.ListAppGroupAccess("m1")
	if len(rules) != 2 {
		t.Fatalf("rules=%v, want 2", rules)
	}

	if _, err := store.ReconcileAppGroupAccessFromManifest("m1", []db.AppGroupRule{
		{Group: "finance", Role: "manager"},
		{Group: "new", Role: "viewer"},
	}); err != nil {
		t.Fatal(err)
	}
	rules, _ = store.ListAppGroupAccess("m1")
	got := map[string]string{}
	for _, r := range rules {
		got[r.Group] = r.Role
		if r.Source != "manifest" {
			t.Fatalf("rule %s source=%s, want manifest", r.Group, r.Source)
		}
	}
	if len(got) != 2 || got["finance"] != "manager" || got["new"] != "viewer" {
		t.Fatalf("after reconcile2 got=%v (leads should be gone)", got)
	}

	if _, err := store.ReconcileAppGroupAccessFromManifest("m1", nil); err != nil {
		t.Fatal(err)
	}
	rules, _ = store.ListAppGroupAccess("m1")
	if len(rules) != 0 {
		t.Fatalf("empty reconcile left %v", rules)
	}
}

func TestReconcileFromManifest_PreservesManual(t *testing.T) {
	store := dbtest.New(t)
	ownerID := agaOwner(t, store)
	store.CreateApp(db.CreateAppParams{Slug: "m2", Name: "m2", OwnerID: ownerID})

	store.GrantAppGroupAccess("m2", "finance", "manager", "manual")

	skipped, err := store.ReconcileAppGroupAccessFromManifest("m2", []db.AppGroupRule{
		{Group: "finance", Role: "viewer"},
		{Group: "analysts", Role: "viewer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || skipped[0] != "finance" {
		t.Fatalf("skipped=%v, want [finance] (manual preempts manifest)", skipped)
	}
	rules, _ := store.ListAppGroupAccess("m2")
	for _, r := range rules {
		if r.Group == "finance" && (r.Role != "manager" || r.Source != "manual") {
			t.Fatalf("manual finance was overwritten: %+v", r)
		}
		if r.Group == "analysts" && r.Source != "manifest" {
			t.Fatalf("analysts should be manifest-sourced: %+v", r)
		}
	}

	store.ReconcileAppGroupAccessFromManifest("m2", nil)
	rules, _ = store.ListAppGroupAccess("m2")
	if len(rules) != 1 || rules[0].Group != "finance" || rules[0].Source != "manual" {
		t.Fatalf("manual row not preserved after empty reconcile: %v", rules)
	}
}

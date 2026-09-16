package db_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestAppEntitlementsSourcesIsolationAndAudit(t *testing.T) {
	s := dbtest.New(t)
	owner := agaOwner(t, s)
	audit := db.AuditEventParams{UserID: &owner}
	app := mustCreateApp(t, s, "finance", owner)
	other := mustCreateApp(t, s, "other", owner)
	if err := s.CreateUser(db.CreateUserParams{Username: "analyst", PasswordHash: "h", Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	u, err := s.GetUserByUsername("analyst")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceUserGroups(u.ID, []string{"finance-team"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{app.ID, other.ID} {
		if err := s.DefineAppEntitlement(id, db.DefineAppEntitlementParams{Name: "power_user"}, audit); err != nil {
			t.Fatal(err)
		}
	}
	user := db.EntitlementPrincipal{UserID: u.ID}
	group := db.EntitlementPrincipal{Group: "finance-team"}
	for _, principal := range []db.EntitlementPrincipal{user, group} {
		if err := s.SetAppEntitlementGrant(app.ID, "power_user", principal, true, audit); err != nil {
			t.Fatal(err)
		}
	}
	assertNames := func(id int64, want []string) {
		t.Helper()
		got, err := s.AppEntitlementsForUser(id, u.ID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("entitlements = %v, %v; want %v", got, err, want)
		}
	}
	assertNames(app.ID, []string{"power_user"})
	assertNames(other.ID, []string{})
	if admitted, err := s.UserCanAccessApp(app.Slug, u.ID); err != nil || admitted {
		t.Fatalf("entitlement granted app admission: %v %v", admitted, err)
	}
	grants, err := s.ListAppEntitlementGrants(app.ID, u.ID)
	if err != nil || len(grants) != 2 {
		t.Fatalf("sources = %v, %v", grants, err)
	}
	if err := s.DeleteAppEntitlement(app.ID, "power_user", audit); !errors.Is(err, db.ErrEntitlementInUse) {
		t.Fatalf("delete used definition: %v", err)
	}
	// An idempotent grant must not create duplicate audit events.
	if err := s.SetAppEntitlementGrant(app.ID, "power_user", user, true, audit); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAppEntitlementGrant(app.ID, "power_user", user, false, audit); err != nil {
		t.Fatal(err)
	}
	assertNames(app.ID, []string{"power_user"})
	if err := s.ReplaceUserGroups(u.ID, nil); err != nil {
		t.Fatal(err)
	}
	assertNames(app.ID, []string{})
	if err := s.SetAppEntitlementGrant(app.ID, "power_user", group, false, audit); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAppEntitlement(app.ID, "power_user", audit); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action LIKE 'entitlement.%' AND resource_id = ?`, app.Slug).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Fatalf("audit event count = %d, want 6 real changes", n)
	}
}

func TestAppEntitlementsAuditFailureRollsBackAndIDsDoNotTransfer(t *testing.T) {
	s := dbtest.New(t)
	owner := agaOwner(t, s)
	app := mustCreateApp(t, s, "app", owner)
	audit := db.AuditEventParams{UserID: &owner}
	if err := s.DefineAppEntitlement(app.ID, db.DefineAppEntitlementParams{Name: "power_user"}, audit); err != nil {
		t.Fatal(err)
	}
	missingActor := int64(999999)
	if err := s.SetAppEntitlementGrant(app.ID, "power_user", db.EntitlementPrincipal{UserID: owner}, true, db.AuditEventParams{UserID: &missingActor}); err == nil {
		t.Fatal("grant succeeded without its audit event")
	}
	got, err := s.AppEntitlementsForUser(app.ID, owner)
	if err != nil || len(got) != 0 {
		t.Fatalf("failed transaction left a grant: %v %v", got, err)
	}
	if err := s.SetAppEntitlementGrant(app.ID, "power_user", db.EntitlementPrincipal{UserID: owner}, true, audit); err != nil {
		t.Fatal(err)
	}
	// Make audit insertion fail after a valid actor and entitlement mutation.
	if _, err := s.DB().Exec(`ALTER TABLE audit_events RENAME TO audit_events_unavailable`); err != nil {
		t.Fatal(err)
	}
	revokeErr := s.SetAppEntitlementGrant(app.ID, "power_user", db.EntitlementPrincipal{UserID: owner}, false, audit)
	if _, err := s.DB().Exec(`ALTER TABLE audit_events_unavailable RENAME TO audit_events`); err != nil {
		t.Fatal(err)
	}
	if revokeErr == nil {
		t.Fatal("revoke succeeded while audit storage was unavailable")
	}
	got, err = s.AppEntitlementsForUser(app.ID, owner)
	if err != nil || !reflect.DeepEqual(got, []string{"power_user"}) {
		t.Fatalf("failed audit did not roll back revocation: %v %v", got, err)
	}
	if err := s.DeleteApp(app.Slug); err != nil {
		t.Fatal(err)
	}
	replacement := mustCreateApp(t, s, "app", owner)
	if replacement.ID == app.ID {
		t.Fatal("app ID was reused")
	}
	got, err = s.AppEntitlementsForUser(replacement.ID, owner)
	if err != nil || len(got) != 0 {
		t.Fatalf("replacement inherited grant: %v %v", got, err)
	}
}

func TestAppEntitlementsValidationAndLimit(t *testing.T) {
	s := dbtest.New(t)
	owner := agaOwner(t, s)
	app := mustCreateApp(t, s, "app", owner)
	audit := db.AuditEventParams{UserID: &owner}
	for _, name := range []string{"", "PowerUser", "power,user", "../admin", "a\n"} {
		if err := s.DefineAppEntitlement(app.ID, db.DefineAppEntitlementParams{Name: name}, audit); !errors.Is(err, db.ErrInvalidEntitlement) {
			t.Fatalf("accepted %q: %v", name, err)
		}
	}
	if err := s.SetAppEntitlementGrant(app.ID, "undefined", db.EntitlementPrincipal{UserID: owner}, true, audit); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("undefined grant: %v", err)
	}
	for i := range db.MaxAppEntitlements {
		if err := s.DefineAppEntitlement(app.ID, db.DefineAppEntitlementParams{Name: fmt.Sprintf("role_%d", i)}, audit); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DefineAppEntitlement(app.ID, db.DefineAppEntitlementParams{Name: "overflow"}, audit); !errors.Is(err, db.ErrEntitlementLimit) {
		t.Fatalf("limit: %v", err)
	}
	description := "updated"
	if err := s.DefineAppEntitlement(app.ID, db.DefineAppEntitlementParams{Name: "role_0", Description: &description}, audit); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAppEntitlementGrant(app.ID, "role_0", db.EntitlementPrincipal{UserID: owner, Group: "team"}, true, audit); !errors.Is(err, db.ErrInvalidEntitlement) {
		t.Fatalf("ambiguous principal: %v", err)
	}
}

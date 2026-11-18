package db_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	policy "github.com/rvben/shinyhub/internal/entitlementpolicy"
)

func TestEntitlementPolicyMigrationKeepsExistingRowsManual(t *testing.T) {
	store := dbtest.New(t)
	userID := agaOwner(t, store)
	app := mustCreateApp(t, store, "policy", userID)
	audit := db.AuditEventParams{UserID: &userID}
	if err := store.DefineAppEntitlement(app.ID, db.DefineAppEntitlementParams{Name: "power"}, audit); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAppEntitlementGrant(app.ID, "power", db.EntitlementPrincipal{Group: "Team"}, true, audit); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"app_entitlements", "app_group_entitlements"} {
		if _, err := store.DB().Exec("ALTER TABLE " + table + " DROP COLUMN managed_by"); err != nil {
			t.Fatal(err)
		}
	}
	backend := "sqlite"
	if store.IsPostgres() {
		backend = "postgres"
	}
	migration, err := os.ReadFile("migrations/" + backend + "/109_entitlement_policy.sql")
	if err != nil {
		t.Fatal(err)
	}
	for statement := range strings.SplitSeq(strings.TrimSpace(string(migration)), ";") {
		if strings.TrimSpace(statement) != "" {
			if _, err := store.DB().Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
	}
	preview, err := store.PreviewEntitlementPolicy(t.Context(), app.ID, "fleet:primary", policy.Policy{})
	if err != nil || len(preview.Changes) != 0 || len(preview.Unmanaged) != 2 {
		t.Fatalf("preexisting rows weren't retained: %+v, %v", preview, err)
	}
	for _, row := range preview.Unmanaged {
		if row.Owner != policy.Manual {
			t.Fatalf("preexisting row wasn't manual: %+v", row)
		}
	}
}

func TestEntitlementPolicyStaleOwnershipAndAtomicAudit(t *testing.T) {
	store := dbtest.New(t)
	userID := agaOwner(t, store)
	app := mustCreateApp(t, store, "policy", userID)
	audit := db.AuditEventParams{UserID: &userID}
	const owner = "fleet:primary"
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	preview := func(p policy.Policy) policy.Preview {
		t.Helper()
		result, err := store.PreviewEntitlementPolicy(t.Context(), app.ID, owner, p)
		check(err)
		return result
	}
	check(store.SetAppFleetOwner(app.ID, owner))
	desired := policy.Policy{"power": {Groups: []string{"Team"}}}
	before := preview(desired)
	check(store.DefineAppEntitlement(app.ID, db.DefineAppEntitlementParams{Name: "manual"}, audit))
	if err := store.ApplyEntitlementPolicy(app.ID, owner, before.Token, desired, audit); !errors.Is(err, db.ErrEntitlementStale) {
		t.Fatalf("stale preview apply: %v", err)
	}
	current := preview(desired)
	check(store.ApplyEntitlementPolicy(app.ID, owner, current.Token, desired, audit))
	if grants, err := store.ListAppEntitlementGrants(app.ID, 0); err != nil || len(grants) != 1 || grants[0].ManagedBy != owner {
		t.Fatalf("grants = %+v, %v", grants, err)
	}
	if err := store.SetAppFleetOwner(app.ID, "fleet:foreign"); !errors.Is(err, db.ErrEntitlementOwnership) {
		t.Fatalf("takeover of managed policy: %v", err)
	}
	if err := store.SetAppEntitlementGrant(app.ID, "power", db.EntitlementPrincipal{Group: "Team"}, false, audit); !errors.Is(err, db.ErrEntitlementOwnership) {
		t.Fatalf("imperative revoke of managed grant: %v", err)
	}
	changedDescription := "change"
	if err := store.DefineAppEntitlement(app.ID, db.DefineAppEntitlementParams{Name: "power", Description: &changedDescription}, audit); !errors.Is(err, db.ErrEntitlementOwnership) {
		t.Fatalf("imperative change of managed definition: %v", err)
	}
	if err := store.DeleteAppEntitlement(app.ID, "power", audit); !errors.Is(err, db.ErrEntitlementOwnership) {
		t.Fatalf("imperative deletion of managed definition: %v", err)
	}
	if err := store.ReplaceUserGroups(userID, []string{"team"}); err != nil {
		t.Fatal(err)
	}
	if names, err := store.AppEntitlementsForUser(app.ID, userID); err != nil || len(names) != 0 {
		t.Fatalf("group names should match verbatim: %v, %v", names, err)
	}
	current = preview(desired)
	if len(current.Changes) != 0 {
		t.Fatalf("already reconciled: %+v", current.Changes)
	}
	check(store.ApplyEntitlementPolicy(app.ID, owner, current.Token, desired, audit))
	var events int
	check(store.DB().QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action LIKE 'entitlement.%'`).Scan(&events))
	if events != 3 {
		t.Fatalf("expected three security mutations, got %d", events)
	}

	cleared := policy.Policy{}
	current = preview(cleared)
	_, err := store.DB().Exec(`ALTER TABLE audit_events RENAME TO audit_events_unavailable`)
	check(err)
	if err := store.ApplyEntitlementPolicy(app.ID, owner, current.Token, cleared, audit); err == nil {
		t.Fatal("policy write succeeded without audit")
	}
	if grants, err := store.ListAppEntitlementGrants(app.ID, 0); err != nil || len(grants) != 1 {
		t.Fatalf("audit failure partially revoked policy: %+v %v", grants, err)
	}
	_, err = store.DB().Exec(`ALTER TABLE audit_events_unavailable RENAME TO audit_events`)
	check(err)
	check(store.RelinquishEntitlementPolicy(app.ID, owner, audit))
	check(store.SetAppFleetOwner(app.ID, "fleet:foreign"))
	if grants, err := store.ListAppEntitlementGrants(app.ID, 0); err != nil || len(grants) != 1 || grants[0].ManagedBy != policy.Manual {
		t.Fatalf("relinquishment changed effective policy: %+v %v", grants, err)
	}
}

func TestEntitlementPolicyPreviewCannotApplyToReplacementApp(t *testing.T) {
	store := dbtest.New(t)
	userID := agaOwner(t, store)
	app := mustCreateApp(t, store, "policy", userID)
	const owner = "fleet:primary"
	desired := policy.Policy{"power": {Groups: []string{"Team"}}}
	preview, err := store.PreviewEntitlementPolicy(t.Context(), app.ID, owner, desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteApp(app.Slug); err != nil {
		t.Fatal(err)
	}
	replacement := mustCreateApp(t, store, app.Slug, userID)
	if err := store.SetAppFleetOwner(replacement.ID, owner); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyEntitlementPolicy(replacement.ID, owner, preview.Token, desired, db.AuditEventParams{UserID: &userID}); !errors.Is(err, db.ErrEntitlementStale) {
		t.Fatalf("apply against a replacement app: %v", err)
	}
}

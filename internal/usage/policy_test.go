package usage

import (
	"database/sql"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func seedPolicyFixture(t *testing.T) (*db.Store, *db.App, int64) {
	t.Helper()
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "hash", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "privacy-app", Name: "Privacy", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("privacy-app")
	if err != nil {
		t.Fatal(err)
	}
	return store, app, owner.ID
}

func TestPolicyPseudonymsAreStablePerAppAndDowngradesScrub(t *testing.T) {
	store, app, userID := seedPolicyFixture(t)
	policy, err := LoadOrInitPolicy(store, config.UsageIdentityIdentified, "test-secret-at-least-thirty-two-bytes")
	if err != nil {
		t.Fatal(err)
	}
	if got, again := policy.Pseudonym(app.Slug, userID), policy.Pseudonym(app.Slug, userID); got != again || got == "" {
		t.Fatalf("pseudonym stability = %q / %q", got, again)
	}
	if policy.Pseudonym(app.Slug, userID) == policy.Pseudonym("another-app", userID) {
		t.Fatal("same person correlated across apps")
	}
	if err := store.BeginUsageSession(db.UsageSessionStart{
		ID: "named", Slug: app.Slug, UserID: userID, PrincipalKind: "person",
		IdentityMode: "identified", InstanceID: "cp", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := policy.ReconcileApp(store, app.ID, config.UsageIdentityPseudonymous); err != nil {
		t.Fatal(err)
	}
	var uid sql.NullInt64
	var viewer, mode string
	if err := store.DB().QueryRow(`SELECT user_id, viewer_key, identity_mode FROM usage_sessions WHERE id = 'named'`).Scan(&uid, &viewer, &mode); err != nil {
		t.Fatal(err)
	}
	if uid.Valid || viewer == "" || mode != "pseudonymous" {
		t.Fatalf("pseudonymous row user=%v viewer=%q mode=%q", uid, viewer, mode)
	}
	if _, err := policy.ReconcileApp(store, app.ID, config.UsageIdentityUnattributed); err != nil {
		t.Fatal(err)
	}
	var viewerNull sql.NullString
	if err := store.DB().QueryRow(`SELECT user_id, viewer_key, identity_mode FROM usage_sessions WHERE id = 'named'`).Scan(&uid, &viewerNull, &mode); err != nil {
		t.Fatal(err)
	}
	if uid.Valid || viewerNull.Valid || mode != "unattributed" {
		t.Fatalf("unattributed row user=%v viewer=%v mode=%q", uid, viewerNull, mode)
	}
}

func TestLoadPolicyAppliesHubDowngradeBeforeReturning(t *testing.T) {
	store, app, userID := seedPolicyFixture(t)
	if _, err := LoadOrInitPolicy(store, config.UsageIdentityIdentified, "test-secret-at-least-thirty-two-bytes"); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginUsageSession(db.UsageSessionStart{
		ID: "before-restart", Slug: app.Slug, UserID: userID, PrincipalKind: "person",
		IdentityMode: "identified", InstanceID: "cp", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrInitPolicy(store, config.UsageIdentityUnattributed, "test-secret-at-least-thirty-two-bytes"); err != nil {
		t.Fatal(err)
	}
	// Simulate a process failure after the mode marker was committed but before
	// every row was scrubbed. The next startup must repair the residue even
	// though the stored mode already equals the configured mode.
	if err := store.BeginUsageSession(db.UsageSessionStart{
		ID: "interrupted-downgrade-residue", Slug: app.Slug, UserID: userID,
		PrincipalKind: "person", IdentityMode: "identified", InstanceID: "cp",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrInitPolicy(store, config.UsageIdentityUnattributed, "test-secret-at-least-thirty-two-bytes"); err != nil {
		t.Fatal(err)
	}
	var identifiers int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM usage_sessions WHERE user_id IS NOT NULL OR viewer_key IS NOT NULL`).Scan(&identifiers); err != nil {
		t.Fatal(err)
	}
	if identifiers != 0 {
		t.Fatalf("hub downgrade left %d identifying rows", identifiers)
	}
}

func TestLoadPolicyRepairsEveryStricterAppOverride(t *testing.T) {
	store, firstApp, userID := seedPolicyFixture(t)
	apps := []struct {
		app      *db.App
		override string
		wantMode string
		wantKey  bool
	}{
		{app: firstApp, override: "pseudonymous", wantMode: "pseudonymous", wantKey: true},
	}
	for _, spec := range []struct{ slug, override, wantMode string }{
		{slug: "unattributed-app", override: "unattributed", wantMode: "unattributed"},
		{slug: "disabled-app", override: "disabled", wantMode: "unattributed"},
	} {
		if _, err := store.CreateApp(db.CreateAppParams{Slug: spec.slug, Name: spec.slug, OwnerID: userID}); err != nil {
			t.Fatal(err)
		}
		app, err := store.GetAppBySlug(spec.slug)
		if err != nil {
			t.Fatal(err)
		}
		apps = append(apps, struct {
			app      *db.App
			override string
			wantMode string
			wantKey  bool
		}{app: app, override: spec.override, wantMode: spec.wantMode})
	}
	for i := range apps {
		override := apps[i].override
		if _, _, _, _, _, err := store.PatchAppSettings(db.PatchAppSettingsParams{
			Slug: apps[i].app.Slug, SetUsageIdentityMode: true, UsageIdentityMode: &override,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.BeginUsageSession(db.UsageSessionStart{
			ID: "residue-" + apps[i].app.Slug, Slug: apps[i].app.Slug, UserID: userID,
			PrincipalKind: "person", IdentityMode: "identified", InstanceID: "cp",
			StartedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadOrInitPolicy(store, config.UsageIdentityIdentified, "test-secret-at-least-thirty-two-bytes"); err != nil {
		t.Fatal(err)
	}
	for _, app := range apps {
		var uid sql.NullInt64
		var key sql.NullString
		var mode string
		if err := store.DB().QueryRow(`SELECT user_id, viewer_key, identity_mode FROM usage_sessions WHERE id = ?`,
			"residue-"+app.app.Slug).Scan(&uid, &key, &mode); err != nil {
			t.Fatal(err)
		}
		if uid.Valid || key.Valid != app.wantKey || mode != app.wantMode {
			t.Fatalf("startup repair %s: user=%v key=%v mode=%s", app.override, uid, key, mode)
		}
	}
}

func TestPolicySnapshotIsCoherentAcrossInstancesAndSlugReuse(t *testing.T) {
	store, app, ownerID := seedPolicyFixture(t)
	const secret = "test-secret-at-least-thirty-two-bytes"
	first, err := LoadOrInitPolicy(store, config.UsageIdentityIdentified, secret)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrInitPolicy(store, config.UsageIdentityIdentified, secret)
	if err != nil {
		t.Fatal(err)
	}

	disabled := "disabled"
	if _, _, _, _, _, err := store.PatchAppSettings(db.PatchAppSettingsParams{
		Slug: app.Slug, SetUsageIdentityMode: true, UsageIdentityMode: &disabled,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.ApplyCommittedAppPolicy(store, app.ID, app.Slug, disabled); err != nil {
		t.Fatal(err)
	}
	// The connection fast path is deliberately database-free. Until its bounded
	// refresh, a remote downgrade may be stale in memory; BeginUsageSessionWithPolicy
	// remains the authoritative write-time clamp.
	if cached := second.CachedSnapshot(app.Slug); !cached.Collect {
		t.Fatal("remote policy unexpectedly mutated another process's cache")
	}
	if err := second.Refresh(); err != nil {
		t.Fatal(err)
	}
	if cached := second.CachedSnapshot(app.Slug); cached.Collect {
		t.Fatal("refreshed cache retained a disabled app")
	}
	snapshot, err := second.Snapshot(app.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Collect {
		t.Fatal("second policy instance retained a stale enabled snapshot")
	}

	if err := store.DeleteApp(app.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: app.Slug, Name: "Recreated", OwnerID: ownerID}); err != nil {
		t.Fatal(err)
	}
	if err := second.Refresh(); err != nil {
		t.Fatal(err)
	}
	if cached := second.CachedSnapshot(app.Slug); !cached.Collect || cached.IdentityMode != config.UsageIdentityIdentified {
		t.Fatalf("recreated slug inherited stale cached policy: %+v", cached)
	}
	snapshot, err = second.Snapshot(app.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Collect || snapshot.IdentityMode != config.UsageIdentityIdentified {
		t.Fatalf("recreated slug inherited stale policy: %+v", snapshot)
	}
}

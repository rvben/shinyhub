package main

import (
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func storedProducerFixture(t *testing.T, onSuccess, deployTrigger string, enabled bool) (*db.Store, *db.App) {
	t.Helper()
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "hash", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: "legacy-app", Name: "Legacy app", ProjectSlug: "legacy", OwnerID: owner.ID, Access: "private",
	}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("legacy-app")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: app.ID, Name: "refresh", CronExpr: "0 5 * * *", CommandJSON: `["producer"]`,
		Enabled: enabled, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip",
		DeployTrigger: deployTrigger, OnSuccess: onSuccess, RollFallback: "defer",
	}); err != nil {
		t.Fatal(err)
	}
	return store, app
}

func TestValidateStoredProducerTopologyRejectsMigratedDockerRollSchedule(t *testing.T) {
	// v0.12.x supported on_success=roll on local Docker. Migration preserves
	// that declaration and supplies deploy_trigger=never, so startup—not the
	// next cron tick—must explain that this legacy topology is no longer safe.
	store, _ := storedProducerFixture(t, "roll", "never", true)
	runtimeCfg := config.RuntimeConfig{
		DefaultWorkerIsolation: "multiplex",
		Tiers:                  []config.TierConfig{{Name: "local", Runtime: "docker"}},
	}

	err := validateStoredProducerTopology(store, runtimeCfg)
	if err == nil {
		t.Fatal("migrated Docker/multiplex producer topology was accepted")
	}
	for _, want := range []string{`app "legacy-app"`, `schedule "refresh"`, `tier "local"`, `runtime "docker"`, "local native"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain actionable detail %q", err, want)
		}
	}
}

func TestValidateStoredProducerTopologyChecksEverySafetyBoundary(t *testing.T) {
	t.Run("native multiplex is accepted", func(t *testing.T) {
		store, _ := storedProducerFixture(t, "none", "bundle_change", true)
		runtimeCfg := config.RuntimeConfig{
			DefaultWorkerIsolation: "multiplex",
			Tiers:                  []config.TierConfig{{Name: "local", Runtime: "native"}},
		}
		if err := validateStoredProducerTopology(store, runtimeCfg); err != nil {
			t.Fatalf("safe producer topology rejected: %v", err)
		}
	})

	t.Run("elastic isolation is accepted on a local native tier", func(t *testing.T) {
		store, app := storedProducerFixture(t, "none", "bundle_change", true)
		// Empty means inherit the fleet default. Fresh rows historically stored
		// multiplex explicitly, so model an app that deliberately chose inherit.
		if _, err := store.DB().Exec(`UPDATE apps SET worker_isolation = '' WHERE id = ?`, app.ID); err != nil {
			t.Fatal(err)
		}
		runtimeCfg := config.RuntimeConfig{
			DefaultWorkerIsolation: "per_session",
			Tiers:                  []config.TierConfig{{Name: "local", Runtime: "native"}},
		}
		if err := validateStoredProducerTopology(store, runtimeCfg); err != nil {
			t.Fatalf("native elastic producer topology rejected: %v", err)
		}
	})

	t.Run("elastic spawn tier that is not native is rejected", func(t *testing.T) {
		// Elastic workers always start on the default tier, whatever the app's
		// placement says, so that tier is the one that must be native.
		store, app := storedProducerFixture(t, "none", "bundle_change", true)
		if err := store.SetAppPlacement(app.ID, `{"gpu":1}`, 1); err != nil {
			t.Fatal(err)
		}
		runtimeCfg := config.RuntimeConfig{
			DefaultWorkerIsolation: "multiplex",
			Tiers:                  []config.TierConfig{{Name: "local", Runtime: "docker"}, {Name: "gpu", Runtime: "native"}},
		}
		if err := validateStoredProducerTopology(store, runtimeCfg); err != nil {
			t.Fatalf("multiplex producer placed on a native tier rejected: %v", err)
		}
		if _, err := store.DB().Exec(`UPDATE apps SET worker_isolation = '' WHERE id = ?`, app.ID); err != nil {
			t.Fatal(err)
		}
		runtimeCfg.DefaultWorkerIsolation = "per_session"
		err := validateStoredProducerTopology(store, runtimeCfg)
		if err == nil || !strings.Contains(err.Error(), `tier "local"`) || !strings.Contains(err.Error(), `runtime "docker"`) {
			t.Fatalf("elastic producer on a docker spawn tier: error = %v, want the spawn tier named", err)
		}
	})

	t.Run("uncleared orphan risk does not block startup", func(t *testing.T) {
		// The marker records a runtime fact, not a configuration: after process
		// recovery the server discharges it itself once no consumer holds the
		// app's lifetime lock, and producer enablement checks it until then.
		store, app := storedProducerFixture(t, "none", "bundle_change", true)
		if err := store.MarkElasticOrphanRisk(app.ID); err != nil {
			t.Fatal(err)
		}
		runtimeCfg := config.RuntimeConfig{
			DefaultWorkerIsolation: "multiplex",
			Tiers:                  []config.TierConfig{{Name: "local", Runtime: "native"}},
		}
		if err := validateStoredProducerTopology(store, runtimeCfg); err != nil {
			t.Fatalf("orphan-risk marker blocked startup: %v", err)
		}
	})

	t.Run("unresolved placed tier is rejected", func(t *testing.T) {
		store, app := storedProducerFixture(t, "none", "bundle_change", true)
		if err := store.SetAppPlacement(app.ID, `{"removed":1}`, 1); err != nil {
			t.Fatal(err)
		}
		runtimeCfg := config.RuntimeConfig{
			DefaultWorkerIsolation: "multiplex",
			Tiers:                  []config.TierConfig{{Name: "local", Runtime: "native"}},
		}
		err := validateStoredProducerTopology(store, runtimeCfg)
		if err == nil || !strings.Contains(err.Error(), `unresolved tier "removed"`) {
			t.Fatalf("unresolved-tier producer error = %v", err)
		}
	})

	t.Run("disabled legacy producer does not block startup", func(t *testing.T) {
		store, _ := storedProducerFixture(t, "roll", "never", false)
		runtimeCfg := config.RuntimeConfig{
			DefaultWorkerIsolation: "multiplex",
			Tiers:                  []config.TierConfig{{Name: "local", Runtime: "docker"}},
		}
		if err := validateStoredProducerTopology(store, runtimeCfg); err != nil {
			t.Fatalf("disabled producer blocked startup: %v", err)
		}
	})
}

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func TestResolveLegacyWritersRejectsNewerSchemaBeforeMutation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.sqlite")
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "hash", Role: "developer"}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: "legacy-app", Name: "Legacy app", ProjectSlug: "legacy", OwnerID: owner.ID, Access: "private",
	}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("legacy-app")
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: app.ID, Name: "refresh", CronExpr: "0 5 * * *", CommandJSON: `["producer"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip",
		DeployTrigger: "never", OnSuccess: "roll", RollFallback: "defer",
	})
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	var runID int64
	if err := store.DB().QueryRow(`
		INSERT INTO schedule_runs (schedule_id, status, trigger, started_at, finished_at, on_success)
		VALUES (?, 'interrupted', 'schedule', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'roll')
		RETURNING id`, scheduleID).Scan(&runID); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO legacy_unfenced_schedule_runs (run_id) VALUES (?)`, runID); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	latest, err := db.LatestSchemaVersion()
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, 'future', '2099-01-01T00:00:00Z')`,
		latest+1,
	); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	configFile := filepath.Join(t.TempDir(), "shinyhub.yaml")
	if err := os.WriteFile(configFile, []byte(fmt.Sprintf("database:\n  dsn: %q\n", dbPath)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHINYHUB_DATABASE_DSN", "")
	t.Setenv("SHINYHUB_CONFIG", "")
	previousConfigPath, previousAck := configPath, resolveLegacyWritersAck
	configPath, resolveLegacyWritersAck = configFile, true
	t.Cleanup(func() {
		configPath, resolveLegacyWritersAck = previousConfigPath, previousAck
	})

	err = resolveLegacyWritersCmd.RunE(resolveLegacyWritersCmd, nil)
	if !errors.Is(err, db.ErrSchemaTooNew) {
		t.Fatalf("resolver error = %v, want ErrSchemaTooNew", err)
	}

	check, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	if count, err := check.CountLegacyUnfencedScheduleRuns(); err != nil || count != 1 {
		t.Fatalf("legacy marker count after rejected resolver = %d, %v; want 1", count, err)
	}
	var uncertainty int
	if err := check.DB().QueryRow(`SELECT COUNT(*) FROM schedule_data_uncertainty`).Scan(&uncertainty); err != nil {
		t.Fatal(err)
	}
	if uncertainty != 0 {
		t.Fatalf("resolver mutated uncertainty before schema rejection: rows=%d", uncertainty)
	}
}

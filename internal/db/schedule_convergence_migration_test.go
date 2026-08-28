package db

import "testing"

// TestMigration069PreservesExistingScheduleHistory exercises the real 068 to
// current upgrade path. Existing declarations and audit history must survive while
// the new convergence/provenance fields receive deliberately unsatisfied safe
// defaults; an upgrade must never infer bundle readiness from lifetime success.
func TestMigration069PreservesExistingScheduleHistory(t *testing.T) {
	t.Parallel()
	s := migratedThrough(t, 68)
	mustExec(t, s, `INSERT INTO users (username, password_hash, role) VALUES ('owner','x','admin')`)
	mustExec(t, s, `INSERT INTO apps (slug, name, owner_id, access) VALUES ('legacy-app','Legacy app',1,'private')`)
	mustExec(t, s, `INSERT INTO app_schedules
		(app_id, name, cron_expr, command_json, enabled, timeout_seconds, overlap_policy, missed_policy, on_success)
		VALUES (1,'refresh','0 5 * * *','["producer"]',1,60,'skip','skip','roll')`)
	mustExec(t, s, `INSERT INTO schedule_runs
		(schedule_id, status, trigger, started_at, finished_at, exit_code, on_success)
		VALUES (1,'succeeded','register','2026-08-25T11:42:15Z','2026-08-25T11:45:00Z',0,'roll')`)
	mustExec(t, s, `INSERT INTO schedule_runs
		(schedule_id, status, trigger, started_at, on_success)
		VALUES (1,'running','schedule','2026-08-28T05:00:00Z','roll')`)
	mustExec(t, s, `INSERT INTO schedule_activations
		(app_id, app_slug, schedule_id, schedule_name, schedule_run_id, action,
		 target_generation, status, due_at)
		VALUES (1,'legacy-app',1,'refresh',1,'roll',1,'succeeded','2026-08-25T11:45:00Z')`)

	if err := s.Migrate(); err != nil {
		t.Fatalf("migrate 068 to 069: %v", err)
	}

	var scheduleName, deployTrigger string
	if err := s.DB().QueryRow(`SELECT name, deploy_trigger FROM app_schedules WHERE id = 1`).Scan(&scheduleName, &deployTrigger); err != nil {
		t.Fatal(err)
	}
	if scheduleName != "refresh" || deployTrigger != "never" {
		t.Fatalf("upgraded schedule = %q/%q, want refresh/never", scheduleName, deployTrigger)
	}
	var runStatus, runVersion, runDigest, runFingerprint, runCommand string
	var publishesData int
	if err := s.DB().QueryRow(`SELECT status, app_version, content_digest, producer_fingerprint, producer_command_json, publishes_data
		FROM schedule_runs WHERE id = 1`).Scan(&runStatus, &runVersion, &runDigest, &runFingerprint, &runCommand, &publishesData); err != nil {
		t.Fatal(err)
	}
	if runStatus != "succeeded" || runVersion != "" || runDigest != "" || runFingerprint != "" || runCommand != "" || publishesData != 0 {
		t.Fatalf("upgraded run = %q/%q/%q/%q/%q publishes=%d", runStatus, runVersion, runDigest, runFingerprint, runCommand, publishesData)
	}
	var activationStatus, sourceVersion, sourceDigest, sourceFingerprint string
	if err := s.DB().QueryRow(`SELECT status, source_app_version, source_content_digest, source_producer_fingerprint
		FROM schedule_activations WHERE id = 1`).Scan(&activationStatus, &sourceVersion, &sourceDigest, &sourceFingerprint); err != nil {
		t.Fatal(err)
	}
	if activationStatus != "succeeded" || sourceVersion != "" || sourceDigest != "" || sourceFingerprint != "" {
		t.Fatalf("upgraded activation = %q/%q/%q/%q", activationStatus, sourceVersion, sourceDigest, sourceFingerprint)
	}
	for _, table := range []string{"schedule_producer_state", "schedule_deploy_obligations"} {
		var count int
		if err := s.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("new table %s unavailable: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("new table %s contains inferred readiness rows", table)
		}
	}
	var legacyUnfenced int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM legacy_unfenced_schedule_runs`).Scan(&legacyUnfenced); err != nil {
		t.Fatal(err)
	}
	if legacyUnfenced != 1 {
		t.Fatalf("legacy unfenced running markers=%d, want 1", legacyUnfenced)
	}

	// A pre-069 server that remains alive during tableflip omits every new
	// provenance column. Migration must close that admission race atomically in
	// the database, after the initial running-row snapshot.
	if _, err := s.DB().Exec(`INSERT INTO schedule_runs
		(schedule_id, status, trigger, started_at, on_success)
		VALUES (1,'running','schedule','2026-08-28T06:00:00Z','roll')`); err == nil {
		t.Fatal("legacy-shaped running admission succeeded after migration")
	}
}

func TestResolveLegacyUnfencedScheduleRunsCreatesRepairQuarantineAtomically(t *testing.T) {
	t.Parallel()
	s := migratedThrough(t, 68)
	mustExec(t, s, `INSERT INTO users (username, password_hash, role) VALUES ('owner','x','admin')`)
	mustExec(t, s, `INSERT INTO apps (slug, name, owner_id, access, status) VALUES ('legacy-app','Legacy app',1,'private','running')`)
	mustExec(t, s, `INSERT INTO app_schedules
		(app_id, name, cron_expr, command_json, enabled, timeout_seconds, overlap_policy, missed_policy, on_success)
		VALUES (1,'refresh','0 5 * * *','["producer"]',1,60,'skip','skip','roll')`)
	mustExec(t, s, `INSERT INTO schedule_runs
		(schedule_id, status, trigger, started_at, on_success)
		VALUES (1,'running','schedule','2026-08-28T05:00:00Z','roll')`)
	if err := s.Migrate(); err != nil {
		t.Fatalf("migrate legacy writer fixture: %v", err)
	}

	legacy, err := s.ListLegacyUnfencedScheduleRuns()
	if err != nil || len(legacy) != 1 {
		t.Fatalf("legacy writers=%+v err=%v, want one", legacy, err)
	}
	if legacy[0].AppSlug != "legacy-app" || legacy[0].ScheduleName != "refresh" || legacy[0].RunStatus != "running" {
		t.Fatalf("legacy writer identity=%+v", legacy[0])
	}
	if _, err := s.DB().Exec(`DELETE FROM schedule_runs WHERE id = ?`, legacy[0].RunID); err == nil {
		t.Fatal("legacy writer run was pruned while its durable fence still existed")
	}
	resolved, err := s.ResolveLegacyUnfencedScheduleRuns()
	if err != nil || resolved != 1 {
		t.Fatalf("ResolveLegacyUnfencedScheduleRuns=%d err=%v", resolved, err)
	}
	if count, err := s.CountLegacyUnfencedScheduleRuns(); err != nil || count != 0 {
		t.Fatalf("legacy fence count=%d err=%v, want zero", count, err)
	}
	var status string
	if err := s.DB().QueryRow(`SELECT status FROM schedule_runs WHERE id = ?`, legacy[0].RunID).Scan(&status); err != nil || status != "interrupted" {
		t.Fatalf("resolved run status=%q err=%v, want interrupted", status, err)
	}
	var uncertaintyStatus string
	if err := s.DB().QueryRow(`SELECT status FROM schedule_data_uncertainty WHERE schedule_id = ?`, legacy[0].ScheduleID).Scan(&uncertaintyStatus); err != nil || uncertaintyStatus != "legacy_unfenced" {
		t.Fatalf("uncertainty=%q err=%v, want legacy_unfenced", uncertaintyStatus, err)
	}
	quarantined, err := s.AppCompatibilityQuarantined(legacy[0].AppID)
	if err != nil || !quarantined {
		t.Fatalf("compatibility quarantined=%v err=%v, want true", quarantined, err)
	}
	if _, err := s.DB().Exec(`DELETE FROM schedule_runs WHERE id = ?`, legacy[0].RunID); err != nil {
		t.Fatalf("resolved legacy run remained unprunable: %v", err)
	}
}

func TestMigration070PreservesDeploymentsAndReplicaAttribution(t *testing.T) {
	t.Parallel()
	s := migratedThrough(t, 69)
	mustExec(t, s, `INSERT INTO users (username, password_hash, role) VALUES ('owner','x','admin')`)
	mustExec(t, s, `INSERT INTO apps (slug, name, owner_id, access) VALUES ('legacy-app','Legacy app',1,'private')`)
	mustExec(t, s, `INSERT INTO deployments (app_id, version, bundle_dir, status, content_digest)
		VALUES (1,'v1','/b/v1','succeeded','sha256:v1')`)
	mustExec(t, s, `INSERT INTO replicas (app_id, idx, status, app_version, deployment_id, data_generation)
		VALUES (1,0,'running','v1',1,7)`)
	if err := s.Migrate(); err != nil {
		t.Fatalf("migrate 069 to 070: %v", err)
	}
	var barrier, snapshotRecorded, priorSnapshotRecorded int
	if err := s.DB().QueryRow(`SELECT producer_barrier_entered, schedule_snapshot_recorded,
		prior_schedule_snapshot_recorded FROM deployments WHERE id = 1`).
		Scan(&barrier, &snapshotRecorded, &priorSnapshotRecorded); err != nil {
		t.Fatal(err)
	}
	if barrier != 0 || snapshotRecorded != 0 || priorSnapshotRecorded != 0 {
		t.Fatalf("legacy deployment compatibility defaults=%d/%d/%d, want 0/0/0", barrier, snapshotRecorded, priorSnapshotRecorded)
	}
	var elasticOrphanRisk int
	if err := s.DB().QueryRow(`SELECT elastic_orphan_risk FROM apps WHERE id = 1`).Scan(&elasticOrphanRisk); err != nil {
		t.Fatal(err)
	}
	if elasticOrphanRisk != 0 {
		t.Fatalf("legacy elastic orphan risk=%d, want 0", elasticOrphanRisk)
	}
	var generation int64
	var producerVersion, producerDigest, producerFingerprint string
	if err := s.DB().QueryRow(`SELECT data_generation, data_producer_app_version,
		data_producer_content_digest, data_producer_fingerprint FROM replicas WHERE app_id = 1 AND idx = 0`).
		Scan(&generation, &producerVersion, &producerDigest, &producerFingerprint); err != nil {
		t.Fatal(err)
	}
	if generation != 7 || producerVersion != "" || producerDigest != "" || producerFingerprint != "" {
		t.Fatalf("legacy replica attribution=%d/%q/%q/%q", generation, producerVersion, producerDigest, producerFingerprint)
	}
	for _, table := range []string{"app_data_publication", "deployment_schedule_snapshots", "deployment_prior_schedule_snapshots"} {
		var count int
		if err := s.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("migration 070 table %s unavailable: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("migration 070 table %s unexpectedly inferred rows", table)
		}
	}
}

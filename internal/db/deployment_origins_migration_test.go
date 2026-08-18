package db

import (
	"database/sql"
	"testing"
)

func TestMigration058BackfillsFleetOriginsAndPreservesLegacyRows(t *testing.T) {
	skipIfPostgres(t)
	s := migratedThrough(t, 57)
	mustExec(t, s, `INSERT INTO users (username, password_hash, role) VALUES ('owner','x','admin')`)
	mustExec(t, s, `INSERT INTO users (username, password_hash, role) VALUES ('deployer','x','developer')`)
	mustExec(t, s, `INSERT INTO apps (slug, name, owner_id, access) VALUES ('origin-migration','Origin migration',1,'private')`)
	mustExec(t, s, `INSERT INTO fleet_runs (id, fleet_id, kind, provenance, user_id)
		VALUES ('0123456789abcdef0123456789abcdef','production','fleet_apply','{}',2)`)
	mustExec(t, s, `INSERT INTO deployments (app_id, version, bundle_dir, status, run_id)
		VALUES (1,'2','/bundles/2','succeeded','0123456789abcdef0123456789abcdef')`)
	mustExec(t, s, `INSERT INTO deployments (app_id, version, bundle_dir, status)
		VALUES (1,'1','/bundles/1','succeeded')`)

	if err := s.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	type migratedOrigin struct {
		kind, channel, actor string
		userID               sql.NullInt64
	}
	readOrigin := func(id int64) migratedOrigin {
		t.Helper()
		var got migratedOrigin
		if err := s.DB().QueryRow(`SELECT origin_kind, origin_channel, origin_user_id, origin_actor
			FROM deployments WHERE id = ?`, id).Scan(&got.kind, &got.channel, &got.userID, &got.actor); err != nil {
			t.Fatalf("read deployment %d origin: %v", id, err)
		}
		return got
	}

	fleet := readOrigin(1)
	if fleet.kind != DeploymentOriginFleet || fleet.channel != DeploymentChannelFleet ||
		!fleet.userID.Valid || fleet.userID.Int64 != 2 || fleet.actor != "deployer" {
		t.Fatalf("fleet backfill = %#v", fleet)
	}
	legacy := readOrigin(2)
	if legacy.kind != DeploymentOriginLegacy || legacy.channel != "" || legacy.userID.Valid || legacy.actor != "" {
		t.Fatalf("legacy row = %#v", legacy)
	}

	// Relational attribution follows user deletion, while the immutable actor
	// snapshot continues to explain who performed the original deployment.
	mustExec(t, s, `DELETE FROM users WHERE id = 2`)
	fleet = readOrigin(1)
	if fleet.userID.Valid || fleet.actor != "deployer" {
		t.Fatalf("fleet origin after user deletion = %#v", fleet)
	}
}

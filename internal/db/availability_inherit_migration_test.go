package db

import (
	"reflect"
	"testing"
)

func TestAvailabilityInheritMigrationPreservesIntentAndRelationships(t *testing.T) {
	s := migratedThrough(t, 106)
	mustExec(t, s, `INSERT INTO users (id,username,password_hash,role) VALUES (1,'owner','hash','developer')`)
	mustExec(t, s, `INSERT INTO apps (id,slug,name,owner_id,availability) VALUES (1,'private','Private',1,'off'),(2,'public','Public',1,'public')`)
	mustExec(t, s, `INSERT INTO deployments (id,app_id,version,bundle_dir) VALUES (1,1,'v1','/tmp/bundle')`)
	before, err := s.GetAppBySlug("private")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetAppBySlug("private")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed durable app/revision inputs: before=%+v after=%+v err=%v", before, after, err)
	}
	for id, want := range map[int]string{1: "off", 2: "public"} {
		var intent string
		if err := s.db.QueryRow(`SELECT availability FROM apps WHERE id = ?`, id).Scan(&intent); err != nil || intent != want {
			t.Fatalf("id %d intent %s: %v", id, intent, err)
		}
	}
	// Old writers retain the conservative SQL default and may overwrite inherit.
	mustExec(t, s, `INSERT INTO apps (id,slug,name,owner_id) VALUES (3,'old-writer','Old writer',1)`)
	var intent string
	if err := s.db.QueryRow(`SELECT availability FROM apps WHERE id = 3`).Scan(&intent); err != nil || intent != "off" {
		t.Fatalf("old writer default %s: %v", intent, err)
	}
	mustExec(t, s, `UPDATE apps SET availability='inherit' WHERE id=1`)
	mustExec(t, s, `UPDATE apps SET availability='public',status='stopped' WHERE id=1`)
	if _, err := s.db.Exec(`UPDATE apps SET availability='invalid' WHERE id=1`); err == nil {
		t.Fatal("missing intent constraint")
	}
	var appID int
	if err := s.db.QueryRow(`SELECT app_id FROM deployments WHERE id=1`).Scan(&appID); err != nil || appID != 1 {
		t.Fatalf("lost deployment relationship: %d %v", appID, err)
	}
	if _, err := s.db.Exec(`INSERT INTO deployments (app_id,version,bundle_dir) VALUES (999,'v2','/tmp/bundle')`); err == nil {
		t.Fatal("foreign keys no longer enforced")
	}
}

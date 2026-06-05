package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestBackfillRelativeBundleDirs(t *testing.T) {
	s := dbtest.New(t)
	owner := mustCreateUser(t, s, "owner", "developer")
	app := mustCreateApp(t, s, "rep", owner.ID)
	// Seed a deployment row with a RELATIVE bundle_dir.
	dep, err := s.BeginDeployment(app.ID, "v1", "./data/apps/rep/versions/v1")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.DeploymentsWithRelativeBundleDir()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Slug != "rep" || rows[0].Version != "v1" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
	if err := s.SetDeploymentBundleDir(dep.ID, "/mnt/shared/apps/rep/versions/v1"); err != nil {
		t.Fatal(err)
	}
	rows2, _ := s.DeploymentsWithRelativeBundleDir()
	if len(rows2) != 0 {
		t.Fatalf("row still relative after update: %+v", rows2)
	}
}

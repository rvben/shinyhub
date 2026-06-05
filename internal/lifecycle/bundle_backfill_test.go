package lifecycle_test

import (
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/lifecycle"
)

func TestNormalizeBundleDirs_RewritesRelative(t *testing.T) {
	store := dbtest.New(t)
	app := mustCreateApp(t, store, "myapp-bundle")

	// Seed a deployment with a relative bundle_dir.
	dep, err := store.BeginDeployment(app.ID, "v2", "./data/apps/myapp-bundle/versions/v2")
	if err != nil {
		t.Fatal(err)
	}

	appsDir := "/mnt/shared/apps"
	if err := lifecycle.NormalizeBundleDirs(store, appsDir); err != nil {
		t.Fatal(err)
	}

	// No relative rows remain after normalization.
	rows, err := store.DeploymentsWithRelativeBundleDir()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("row still relative after NormalizeBundleDirs: %+v", rows)
	}

	// The rewritten bundle_dir must equal the canonical reconstructed absolute path.
	want := filepath.Join(appsDir, "myapp-bundle", "versions", "v2")
	got, err := store.GetDeploymentBySlugAndID("myapp-bundle", dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BundleDir != want {
		t.Fatalf("bundle_dir = %q, want %q", got.BundleDir, want)
	}
}

func TestNormalizeBundleDirs_LeavesAbsoluteUntouched(t *testing.T) {
	store := dbtest.New(t)
	app := mustCreateApp(t, store, "myapp-abs")

	// Seed a deployment that already has an absolute bundle_dir.
	_, err := store.BeginDeployment(app.ID, "v1", "/absolute/apps/myapp-abs/versions/v1")
	if err != nil {
		t.Fatal(err)
	}

	if err := lifecycle.NormalizeBundleDirs(store, "/mnt/shared/apps"); err != nil {
		t.Fatal(err)
	}

	// No relative rows - the absolute row is untouched.
	rows, err := store.DeploymentsWithRelativeBundleDir()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("NormalizeBundleDirs touched absolute rows: %+v", rows)
	}
}

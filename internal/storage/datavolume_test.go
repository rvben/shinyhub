package storage_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/storage"
)

func TestLocalVolumeProvisionCreatesDir(t *testing.T) {
	root := t.TempDir()
	var dv storage.DataVolume = storage.LocalVolume{Root: root}

	ref, err := dv.Provision("demo")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	want := filepath.Join(root, "demo")
	if ref.Path != want {
		t.Fatalf("Path = %q; want %q", ref.Path, want)
	}
	info, err := os.Stat(want)
	if err != nil || !info.IsDir() {
		t.Fatalf("expected directory at %q: err=%v", want, err)
	}

	// Idempotent: provisioning again succeeds and returns the same path.
	ref2, err := dv.Provision("demo")
	if err != nil || ref2.Path != want {
		t.Fatalf("second provision: ref=%+v err=%v", ref2, err)
	}
}

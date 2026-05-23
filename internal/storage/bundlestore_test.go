package storage_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/storage"
)

func TestLocalStorePutResolveIsIdentity(t *testing.T) {
	dir := t.TempDir()
	var bs storage.BundleStore = storage.LocalStore{}

	ref, err := bs.Put("demo", "v7", dir)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if ref.Slug != "demo" || ref.Version != "v7" || ref.Path != dir {
		t.Fatalf("ref = %+v; want slug=demo version=v7 path=%s", ref, dir)
	}

	loc, err := bs.Resolve(ref, "local")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if loc.LocalPath != dir {
		t.Fatalf("LocalPath = %q; want %q", loc.LocalPath, dir)
	}
}

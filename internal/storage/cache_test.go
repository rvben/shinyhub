package storage_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/storage"
)

func TestProvisionCache_CreatesPerDeploymentNamespace(t *testing.T) {
	root := t.TempDir()
	p, err := storage.ProvisionCache(root, "demo", 42)
	if err != nil {
		t.Fatalf("ProvisionCache: %v", err)
	}
	if want := filepath.Join(root, "demo", "d42"); p != want {
		t.Fatalf("path = %q, want %q", p, want)
	}
	if st, err := os.Stat(p); err != nil || !st.IsDir() {
		t.Fatalf("namespace not created: %v", err)
	}
	// Idempotent: a restart of the same activation reuses what it cached.
	if err := os.WriteFile(filepath.Join(p, "entry"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.ProvisionCache(root, "demo", 42); err != nil {
		t.Fatalf("second ProvisionCache: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p, "entry")); err != nil {
		t.Fatal("provisioning again discarded the activation's cache")
	}
}

func TestProvisionCache_RejectsMissingDeployment(t *testing.T) {
	if _, err := storage.ProvisionCache(t.TempDir(), "demo", 0); err == nil {
		t.Fatal("a zero deployment id was given a namespace; every unknown activation would share it")
	}
}

func TestPruneCacheNamespaces_KeepsRetainedRemovesRest(t *testing.T) {
	root := t.TempDir()
	for _, id := range []int64{3, 7, 9, 12} {
		if _, err := storage.ProvisionCache(root, "demo", id); err != nil {
			t.Fatal(err)
		}
	}
	// Not namespaces: must survive whatever the retained set says.
	for _, name := range []string{"notes", "d", "d07", "dx1", "d-3"} {
		if err := os.MkdirAll(filepath.Join(root, "demo", name), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "demo", "d5"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Another app's namespace with a matching ID is out of scope.
	if _, err := storage.ProvisionCache(root, "other", 3); err != nil {
		t.Fatal(err)
	}

	removed, err := storage.PruneCacheNamespaces(root, "demo", map[int64]bool{7: true, 12: true}, 12)
	if err != nil {
		t.Fatalf("PruneCacheNamespaces: %v", err)
	}
	slices.Sort(removed)
	if !slices.Equal(removed, []int64{3, 9}) {
		t.Fatalf("removed %v, want [3 9]", removed)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "demo"))
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	want := []string{"d", "d-3", "d07", "d12", "d5", "d7", "dx1", "notes"}
	if !slices.Equal(left, want) {
		t.Fatalf("left %v, want %v", left, want)
	}
	if _, err := os.Stat(storage.CacheNamespace(root, "other", 3)); err != nil {
		t.Fatal("pruning demo removed another app's namespace")
	}
}

// A namespace newer than the snapshot belongs to an activation created after
// the retained set was read; it may be starting right now and must survive.
func TestPruneCacheNamespaces_KeepsNamespacesNewerThanSnapshot(t *testing.T) {
	root := t.TempDir()
	for _, id := range []int64{4, 5, 6} {
		if _, err := storage.ProvisionCache(root, "demo", id); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := storage.PruneCacheNamespaces(root, "demo", map[int64]bool{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(removed, []int64{4}) {
		t.Fatalf("removed %v, want only [4]; 5 and 6 are newer than the snapshot", removed)
	}
}

func TestPruneCacheNamespaces_MissingAppDirIsNotAnError(t *testing.T) {
	removed, err := storage.PruneCacheNamespaces(t.TempDir(), "never-started", nil, 0)
	if err != nil || len(removed) != 0 {
		t.Fatalf("got %v, %v; want nothing removed and no error", removed, err)
	}
}

// The cache is disposable: a leftover must not block recreating the slug the
// way leftover code or data does, and deleting the app must remove it.
func TestAppCache_DeleteAndSlugReuse(t *testing.T) {
	base := t.TempDir()
	cfg := &config.Config{Storage: config.StorageConfig{
		AppsDir:     filepath.Join(base, "apps"),
		AppDataDir:  filepath.Join(base, "data"),
		AppCacheDir: filepath.Join(base, "cache"),
	}}
	if _, err := storage.ProvisionCache(cfg.Storage.AppCacheDir, "demo", 1); err != nil {
		t.Fatal(err)
	}
	if err := storage.RequireFreeSlug(cfg, "demo"); err != nil {
		t.Fatalf("a leftover result cache blocked reusing the slug: %v", err)
	}
	if err := storage.DiscardLeftoverCache(cfg, "demo"); err != nil {
		t.Fatalf("DiscardLeftoverCache: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Storage.AppCacheDir, "demo")); !os.IsNotExist(err) {
		t.Fatal("the leftover cache survived DiscardLeftoverCache")
	}

	if _, err := storage.ProvisionCache(cfg.Storage.AppCacheDir, "demo", 2); err != nil {
		t.Fatal(err)
	}
	if err := storage.OnAppDelete(cfg, "demo"); err != nil {
		t.Fatalf("OnAppDelete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Storage.AppCacheDir, "demo")); !os.IsNotExist(err) {
		t.Fatal("deleting the app left its result cache on disk")
	}
}

func TestCacheRemoval_RefusesTargetsOutsideTheRoot(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "keep")
	if err := os.MkdirAll(keep, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ root, slug string }{
		"relative root": {root: "", slug: "keep"},
		"empty slug":    {root: root, slug: ""},
		"dot slug":      {root: root, slug: "."},
		"parent slug":   {root: filepath.Join(root, "keep"), slug: ".."},
		"nested slug":   {root: root, slug: "a/../keep"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := storage.RemoveAppCache(tc.root, tc.slug); err == nil {
				t.Fatal("removal outside the cache root was accepted")
			}
			if _, err := storage.PruneCacheNamespaces(tc.root, tc.slug, nil, 1<<62); err == nil {
				t.Fatal("prune outside the cache root was accepted")
			}
		})
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("a refused removal still deleted a directory")
	}
}

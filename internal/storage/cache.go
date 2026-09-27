package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/fsx"
)

// A result cache is disposable state an app keeps between its own sessions
// and processes (Shiny's bindCache with a disk-backed cache, diskcache in
// Python). Each activation of an app gets its own namespace,
// <root>/<slug>/d<deploymentID>, so new code and a rollback always start
// empty, while every process of one activation (replicas, elastic workers,
// restarts, hibernation wakes, its scheduled jobs) shares one directory.

// cacheNamespacePrefix starts every namespace directory name. The rest of the
// name is the deployment ID in decimal.
const cacheNamespacePrefix = "d"

// CacheNamespace is the directory an activation of slug running deploymentID
// uses for its result cache.
func CacheNamespace(root, slug string, deploymentID int64) string {
	return filepath.Join(root, slug, cacheNamespacePrefix+strconv.FormatInt(deploymentID, 10))
}

// ProvisionCache creates the namespace for slug's deploymentID and returns its
// path. The directory must exist before launch: a Landlock write rule for a
// missing path is dropped, which would leave the app unable to write to it.
func ProvisionCache(root, slug string, deploymentID int64) (string, error) {
	if deploymentID <= 0 {
		return "", fmt.Errorf("provision result cache for %s: no deployment id", slug)
	}
	p := CacheNamespace(root, slug, deploymentID)
	if err := os.MkdirAll(p, 0o750); err != nil {
		return "", fmt.Errorf("provision result cache for %s: %w", slug, err)
	}
	return p, nil
}

// PruneCacheNamespaces removes every namespace of slug whose deployment is
// not in retained and not above maxID, and reports the IDs it removed. Entries
// that are not namespaces are left alone. Callers pass every deployment a
// process or job could still be using; removing a directory under a live
// cache breaks the app's cache object. maxID is the newest deployment the
// caller knew of when it read retained: anything newer was created after that
// read, so its activation may be starting right now.
func PruneCacheNamespaces(root, slug string, retained map[int64]bool, maxID int64) ([]int64, error) {
	if err := checkCacheTarget(root, slug); err != nil {
		return nil, err
	}
	dir := filepath.Join(root, slug)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var removed []int64
	var errs []error
	for _, e := range entries {
		id, ok := namespaceID(e)
		if !ok || retained[id] || id > maxID {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if err := fsx.RemoveAll(p); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", p, err))
			continue
		}
		removed = append(removed, id)
	}
	return removed, errors.Join(errs...)
}

// CacheRetentionReader reads which of an app's deployments may still be using
// their result cache namespace. *db.Store implements it.
type CacheRetentionReader interface {
	CacheRetention(appID int64) (db.CacheRetention, error)
}

// PruneAppCache removes the namespaces of slug that no process or job can
// still be using. It runs whether or not the cache is currently enabled, so
// turning the cache off does not strand what an earlier configuration wrote.
func PruneAppCache(root string, store CacheRetentionReader, slug string, appID int64) ([]int64, error) {
	if root == "" {
		return nil, nil
	}
	r, err := store.CacheRetention(appID)
	if err != nil {
		return nil, fmt.Errorf("prune result cache for %s: %w", slug, err)
	}
	return PruneCacheNamespaces(root, slug, r.Retained, r.MaxID)
}

// RemoveCacheNamespace removes one activation's namespace, as when the
// activation failed before anything could use it.
func RemoveCacheNamespace(root, slug string, deploymentID int64) error {
	if err := checkCacheTarget(root, slug); err != nil {
		return err
	}
	return fsx.RemoveAll(CacheNamespace(root, slug, deploymentID))
}

// CacheNamespaceIDs lists the deployment IDs slug has a namespace for, in
// ascending order. A slug with no cache directory has none.
func CacheNamespaceIDs(root, slug string) ([]int64, error) {
	if err := checkCacheTarget(root, slug); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(root, slug))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []int64
	for _, e := range entries {
		if id, ok := namespaceID(e); ok {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// RemoveAppCache removes every namespace of slug. It is safe only when no
// process or job of the app is running.
func RemoveAppCache(root, slug string) error {
	if err := checkCacheTarget(root, slug); err != nil {
		return err
	}
	return fsx.RemoveAll(filepath.Join(root, slug))
}

// checkCacheTarget refuses a removal that would not land inside the cache
// root: an empty root would make the path relative to the working directory,
// and a slug that is empty or not a single path element would escape to the
// root itself or beyond it.
func checkCacheTarget(root, slug string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("result cache root %q is not an absolute path", root)
	}
	if slug == "" || slug == "." || slug == ".." || filepath.Base(slug) != slug {
		return fmt.Errorf("invalid app slug %q for result cache removal", slug)
	}
	return nil
}

func namespaceID(e os.DirEntry) (int64, bool) {
	if !e.IsDir() || !strings.HasPrefix(e.Name(), cacheNamespacePrefix) {
		return 0, false
	}
	digits := strings.TrimPrefix(e.Name(), cacheNamespacePrefix)
	id, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != digits {
		return 0, false
	}
	return id, true
}

// cacheFences holds one fence per slug, created on first use.
var cacheFences sync.Map // slug -> *sync.RWMutex

// CacheFence serializes clearing an app's cache against starting it. Every
// launch path holds the read side from choosing the namespace until the
// process or job is registered as running; a clear holds the write side
// while it checks that nothing is running and removes the directory, so no
// start can slip in between the check and the removal.
func CacheFence(slug string) *sync.RWMutex {
	f, _ := cacheFences.LoadOrStore(slug, new(sync.RWMutex))
	return f.(*sync.RWMutex)
}

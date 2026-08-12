package localrun

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

var workspaceExcludedDirs = map[string]bool{
	".git":          true,
	".venv":         true,
	".shinyhub-run": true,
	"__pycache__":   true,
	"node_modules":  true,
	".renv":         true,
	".Rproj.user":   true,
}

// workspace is ShinyHub-owned state for one source checkout. BundleDir is a
// mirror of the developer's source that dependency tools and app processes may
// freely mutate; DataDir is durable local app data. Neither lives in source.
type workspace struct {
	Root      string
	BundleDir string
	DataDir   string
	indexPath string
	dirtyPath string
	slot      int
}

func workspaceFor(sourceDir, stateDir, dataDir string) (*workspace, error) {
	root := stateDir
	if root == "" {
		cacheDir, err := os.UserCacheDir()
		if err != nil || cacheDir == "" {
			cacheDir = os.TempDir()
		}
		digest := sha256.Sum256([]byte(sourceDir))
		root = filepath.Join(cacheDir, "shinyhub", "run", hex.EncodeToString(digest[:8]))
	} else {
		abs, err := canonicalPath(root)
		if err != nil {
			return nil, fmt.Errorf("resolve state dir: %w", err)
		}
		root = abs
	}

	resolvedData := dataDir
	if resolvedData == "" {
		resolvedData = filepath.Join(root, "data")
	} else {
		abs, err := canonicalPath(resolvedData)
		if err != nil {
			return nil, fmt.Errorf("resolve data dir: %w", err)
		}
		resolvedData = abs
	}

	w := &workspace{
		Root:      root,
		BundleDir: filepath.Join(root, "bundles", "0"),
		DataDir:   resolvedData,
		indexPath: filepath.Join(root, "source-index-0.json"),
		dirtyPath: filepath.Join(root, "deps-dirty-0"),
	}
	return w, nil
}

// canonicalPath resolves symlinks in the existing portion of a path while
// still accepting a final directory that has not been created yet.
func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := abs
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return abs, nil
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func (w *workspace) alternate() *workspace {
	slot := 1
	if w.slot == 1 {
		slot = 0
	}
	return &workspace{
		Root:      w.Root,
		BundleDir: filepath.Join(w.Root, "bundles", fmt.Sprintf("%d", slot)),
		DataDir:   w.DataDir,
		indexPath: filepath.Join(w.Root, fmt.Sprintf("source-index-%d.json", slot)),
		dirtyPath: filepath.Join(w.Root, fmt.Sprintf("deps-dirty-%d", slot)),
		slot:      slot,
	}
}

// syncSource mirrors user-owned bundle files into the workspace while keeping
// generated dependency state (.venv, synthesized pyproject/lock) intact. The
// saved source index lets deletions propagate without ever treating generated
// workspace files as source-owned.
func (w *workspace) syncSource(sourceDir string) (depsChanged bool, err error) {
	if err := os.MkdirAll(w.BundleDir, 0o750); err != nil {
		return false, fmt.Errorf("create local workspace bundle: %w", err)
	}
	if err := os.MkdirAll(w.DataDir, 0o750); err != nil {
		return false, fmt.Errorf("create local data dir: %w", err)
	}
	previous, err := readSourceIndex(w.indexPath)
	if err != nil {
		return false, err
	}
	current := make(map[string]sourceEntry)

	err = filepath.WalkDir(sourceDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() && workspaceExcludedDirs[d.Name()] {
			return filepath.SkipDir
		}
		if firstPathSegment(rel) == "data" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		entry := sourceEntry{Path: filepath.ToSlash(rel), Mode: uint32(info.Mode())}
		if info.Mode().IsRegular() && isDependencyInput(rel) {
			sum, err := fileDigest(path)
			if err != nil {
				return err
			}
			entry.Digest = sum
		}
		current[entry.Path] = entry
		return copyEntry(path, filepath.Join(w.BundleDir, rel), info)
	})
	if err != nil {
		return false, fmt.Errorf("sync source into local workspace: %w", err)
	}

	for rel := range previous {
		if _, ok := current[rel]; ok {
			continue
		}
		clean, ok := safeRelativePath(rel)
		if !ok {
			return false, fmt.Errorf("local workspace index contains unsafe path %q", rel)
		}
		if err := os.RemoveAll(filepath.Join(w.BundleDir, clean)); err != nil && !errors.Is(err, syscall.ENOTDIR) {
			return false, fmt.Errorf("remove stale local workspace path %s: %w", rel, err)
		}
	}

	depsChanged = dependencyInputsChanged(previous, current)
	if _, dirtyErr := os.Stat(w.dirtyPath); dirtyErr == nil {
		depsChanged = true
	} else if !errors.Is(dirtyErr, os.ErrNotExist) {
		return false, fmt.Errorf("inspect local dependency state: %w", dirtyErr)
	}
	// A requirements-only workspace owns its synthesized project. Recreate it
	// when requirements change so additions AND removals are faithfully applied.
	if depsChanged {
		_, sourceHasProject := current["pyproject.toml"]
		if !sourceHasProject {
			if _, markerErr := os.Stat(filepath.Join(w.BundleDir, ".shinyhub-synthesized-project")); markerErr == nil {
				for _, name := range []string{"pyproject.toml", "uv.lock", ".shinyhub-synthesized-project", ".venv"} {
					if err := os.RemoveAll(filepath.Join(w.BundleDir, name)); err != nil {
						return false, fmt.Errorf("refresh synthesized local project: %w", err)
					}
				}
			}
		}
	}

	if err := writeSourceIndex(w.indexPath, current); err != nil {
		return false, err
	}
	if err := ensureWorkspaceDataSymlink(filepath.Join(w.BundleDir, "data"), w.DataDir); err != nil {
		return false, err
	}
	return depsChanged, nil
}

// acquireLock prevents two local runners from mutating the same persistent
// mirror. Developers can still run two copies by assigning distinct
// --state-dir values; the default remains safe and deterministic.
func (w *workspace) acquireLock() (func(), error) {
	if err := os.MkdirAll(w.Root, 0o750); err != nil {
		return nil, fmt.Errorf("create local workspace: %w", err)
	}
	path := filepath.Join(w.Root, "run.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open local workspace lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("this app already has a local runner; use --state-dir to run another copy")
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

func (w *workspace) validateLocations(sourceDir string) error {
	bundlesDir := filepath.Join(w.Root, "bundles")
	if pathsOverlap(sourceDir, bundlesDir) {
		return fmt.Errorf("local workspace must be outside the app source: %s", w.Root)
	}
	if pathsOverlap(sourceDir, w.DataDir) {
		return fmt.Errorf("local data directory must be outside the app source: %s", w.DataDir)
	}
	if pathsOverlap(bundlesDir, w.DataDir) {
		return fmt.Errorf("local data directory must be outside generated bundles: %s", w.DataDir)
	}
	return nil
}

func pathsOverlap(a, b string) bool {
	return pathContains(a, b) || pathContains(b, a)
}

func pathContains(parent, candidate string) bool {
	rel, err := filepath.Rel(parent, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (w *workspace) resetBundle() error {
	if err := os.RemoveAll(w.BundleDir); err != nil {
		return fmt.Errorf("clear local workspace bundle: %w", err)
	}
	if err := os.Remove(w.indexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear local workspace index: %w", err)
	}
	if err := os.Remove(w.dirtyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear local dependency marker: %w", err)
	}
	if err := os.MkdirAll(w.BundleDir, 0o750); err != nil {
		return fmt.Errorf("recreate local workspace bundle: %w", err)
	}
	return nil
}

func (w *workspace) markDependenciesDirty() error {
	if err := os.WriteFile(w.dirtyPath, []byte("dependency preparation incomplete\n"), 0o600); err != nil {
		return fmt.Errorf("mark local dependencies for rebuild: %w", err)
	}
	return nil
}

func (w *workspace) markDependenciesReady() error {
	if err := os.Remove(w.dirtyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("commit local dependency preparation: %w", err)
	}
	return nil
}

func (w *workspace) resetAllBundles() error {
	for _, candidate := range []*workspace{w, w.alternate()} {
		if err := candidate.resetBundle(); err != nil {
			return err
		}
	}
	return nil
}

// ensureWorkspaceDataSymlink may retarget the workspace-owned link when a
// developer changes --data-dir between runs. A non-symlink is left untouched
// because it may contain app-created data worth preserving.
func ensureWorkspaceDataSymlink(linkPath, dataDir string) error {
	info, err := os.Lstat(linkPath)
	if err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("local workspace data path is not a symlink; move it aside and retry: %s", linkPath)
		}
		existing, readErr := os.Readlink(linkPath)
		if readErr != nil {
			return fmt.Errorf("read local workspace data symlink: %w", readErr)
		}
		if existing == dataDir {
			return nil
		}
		if err := os.Remove(linkPath); err != nil {
			return fmt.Errorf("retarget local workspace data symlink: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect local workspace data symlink: %w", err)
	}
	if err := os.Symlink(dataDir, linkPath); err != nil {
		return fmt.Errorf("symlink local app data: %w", err)
	}
	return nil
}

type sourceEntry struct {
	Path   string `json:"path"`
	Mode   uint32 `json:"mode"`
	Digest string `json:"digest,omitempty"`
}

func readSourceIndex(path string) (map[string]sourceEntry, error) {
	out := make(map[string]sourceEntry)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read local workspace index: %w", err)
	}
	var entries []sourceEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("parse local workspace index: %w", err)
	}
	for _, entry := range entries {
		out[entry.Path] = entry
	}
	return out, nil
}

func writeSourceIndex(path string, entries map[string]sourceEntry) error {
	ordered := make([]sourceEntry, 0, len(entries))
	for _, entry := range entries {
		ordered = append(ordered, entry)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	b, err := json.MarshalIndent(ordered, "", "  ")
	if err != nil {
		return fmt.Errorf("encode local workspace index: %w", err)
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write local workspace index: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("commit local workspace index: %w", err)
	}
	return nil
}

func dependencyInputsChanged(before, after map[string]sourceEntry) bool {
	for path, entry := range before {
		if !isDependencyInput(path) {
			continue
		}
		other, ok := after[path]
		if !ok || entry.Digest != other.Digest {
			return true
		}
	}
	for path, entry := range after {
		if !isDependencyInput(path) {
			continue
		}
		other, ok := before[path]
		if !ok || entry.Digest != other.Digest {
			return true
		}
	}
	return false
}

func isDependencyInput(rel string) bool {
	clean := filepath.ToSlash(rel)
	switch clean {
	case "requirements.txt", "pyproject.toml", "uv.lock", "renv.lock", ".Rprofile":
		return true
	}
	return strings.HasPrefix(clean, "renv/")
}

func firstPathSegment(rel string) string {
	clean := filepath.ToSlash(rel)
	if i := strings.IndexByte(clean, '/'); i >= 0 {
		return clean[:i]
	}
	return clean
}

func safeRelativePath(rel string) (string, bool) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || clean == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false
	}
	return clean, true
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyEntry(source, dest string, info fs.FileInfo) error {
	if info.IsDir() {
		if existing, err := os.Lstat(dest); err == nil && !existing.IsDir() {
			if err := os.RemoveAll(dest); err != nil {
				return err
			}
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(dest, info.Mode().Perm()); err != nil {
			return err
		}
		return os.Chmod(dest, info.Mode().Perm())
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(dest); err != nil {
			return err
		}
		return os.Symlink(target, dest)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsupported bundle entry %s (%s)", source, info.Mode())
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	if existing, err := os.Lstat(dest); err == nil && existing.IsDir() {
		if err := os.RemoveAll(dest); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp := dest + ".shinyhub-copy"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

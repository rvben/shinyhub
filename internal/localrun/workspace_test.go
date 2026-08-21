package localrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/bundle"
)

func TestWorkspaceSyncKeepsGeneratedStateOutsideSource(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "app.py"), []byte("app = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "requirements.txt"), []byte("shiny\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := workspaceFor(source, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := w.syncSource(source)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first dependency sync must report changed")
	}
	for _, name := range []string{"pyproject.toml", "uv.lock", ".venv", ".shinyhub-synthesized-project"} {
		if err := os.WriteFile(filepath.Join(w.BundleDir, name), []byte("generated\n"), 0o644); err != nil {
			if name == ".venv" {
				continue
			}
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(source, name)); !os.IsNotExist(err) {
			t.Fatalf("generated %s leaked into source", name)
		}
	}
	if _, err := os.Stat(filepath.Join(source, "data")); !os.IsNotExist(err) {
		t.Fatal("workspace data symlink leaked into source")
	}
	if info, err := os.Lstat(filepath.Join(w.BundleDir, "data")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("workspace data link missing: info=%v err=%v", info, err)
	}
}

func TestWorkspaceSyncPropagatesDeletion(t *testing.T) {
	source := t.TempDir()
	path := filepath.Join(source, "helper.py")
	if err := os.WriteFile(path, []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := workspaceFor(source, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.syncSource(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := w.syncSource(source); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.BundleDir, "helper.py")); !os.IsNotExist(err) {
		t.Fatalf("deleted source file survived in workspace: %v", err)
	}
}

func TestWorkspaceSyncHandlesFileDirectoryTypeChanges(t *testing.T) {
	source := t.TempDir()
	path := filepath.Join(source, "changing")
	if err := os.WriteFile(path, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := workspaceFor(source, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.syncSource(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "nested"), []byte("directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.syncSource(source); err != nil {
		t.Fatalf("file to directory sync: %v", err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("file again"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.syncSource(source); err != nil {
		t.Fatalf("directory to file sync: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(w.BundleDir, "changing"))
	if err != nil || string(got) != "file again" {
		t.Fatalf("final workspace entry = %q, %v", got, err)
	}
}

func TestWorkspaceRequirementsChangeInvalidatesSynthesizedProject(t *testing.T) {
	source := t.TempDir()
	requirements := filepath.Join(source, "requirements.txt")
	if err := os.WriteFile(requirements, []byte("shiny\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := workspaceFor(source, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.syncSource(source); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pyproject.toml", "uv.lock", ".shinyhub-synthesized-project"} {
		if err := os.WriteFile(filepath.Join(w.BundleDir, name), []byte("generated\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(w.BundleDir, ".venv"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requirements, []byte("shiny\npandas\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := w.syncSource(source)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("requirements edit was not detected")
	}
	for _, name := range []string{"pyproject.toml", "uv.lock", ".shinyhub-synthesized-project", ".venv"} {
		if _, err := os.Stat(filepath.Join(w.BundleDir, name)); !os.IsNotExist(err) {
			t.Fatalf("stale generated %s survived requirements edit: %v", name, err)
		}
	}
}

func TestWorkspaceRetriesIncompleteDependencyPreparation(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "pyproject.toml"), []byte("[project]\nname='demo'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := workspaceFor(source, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.syncSource(source); err != nil {
		t.Fatal(err)
	}
	if err := w.markDependenciesDirty(); err != nil {
		t.Fatal(err)
	}
	changed, err := w.syncSource(source)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("incomplete preparation did not force a retry")
	}
	if err := w.markDependenciesReady(); err != nil {
		t.Fatal(err)
	}
	changed, err = w.syncSource(source)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("completed preparation remained dirty")
	}
}

func TestWorkspaceDataDirectoryCanChangeBetweenRuns(t *testing.T) {
	source := t.TempDir()
	state := t.TempDir()
	firstData := t.TempDir()
	secondData := t.TempDir()
	w, err := workspaceFor(source, state, firstData)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.syncSource(source); err != nil {
		t.Fatal(err)
	}
	w, err = workspaceFor(source, state, secondData)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.syncSource(source); err != nil {
		t.Fatal(err)
	}
	got, err := os.Readlink(filepath.Join(w.BundleDir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := canonicalPath(secondData)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("data link = %q, want %q", got, want)
	}
}

func TestWorkspaceLockRejectsConcurrentRunner(t *testing.T) {
	w, err := workspaceFor(t.TempDir(), t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	release, err := w.acquireLock()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := w.acquireLock(); err == nil {
		t.Fatal("second runner unexpectedly acquired the same workspace")
	} else if !strings.Contains(err.Error(), "this local workspace already has a runner") {
		t.Fatalf("lock error = %q", err)
	}
}

func TestWorkspaceSyncComposesAndRemovesIndexedInput(t *testing.T) {
	source := t.TempDir()
	w, err := workspaceFor(source, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	first := bundle.FileInputSnapshot{From: "_shared/old.py", To: "helpers/old.py", Mode: 0o644, Data: []byte("old\n")}
	if _, err := w.syncSourceWithInputs(source, []bundle.FileInputSnapshot{first}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(w.BundleDir, "helpers", "old.py")); err != nil || string(got) != "old\n" {
		t.Fatalf("composed input = %q, %v", got, err)
	}
	second := bundle.FileInputSnapshot{From: "_shared/new.py", To: "helpers/new.py", Mode: 0o644, Data: []byte("new\n")}
	if _, err := w.syncSourceWithInputs(source, []bundle.FileInputSnapshot{second}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.BundleDir, "helpers", "old.py")); !os.IsNotExist(err) {
		t.Fatalf("stale input survived composition change: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(w.BundleDir, "helpers", "new.py")); err != nil || string(got) != "new\n" {
		t.Fatalf("replacement input = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(source, "helpers")); !os.IsNotExist(err) {
		t.Fatalf("composition mutated app source: %v", err)
	}
}

func TestWorkspaceInputReplacesMutableSymlinkAncestor(t *testing.T) {
	source := t.TempDir()
	w, err := workspaceFor(source, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	input := bundle.FileInputSnapshot{From: "_shared/theme.py", To: "helpers/theme.py", Mode: 0o644, Data: []byte("orange\n")}
	if _, err := w.syncSourceWithInputs(source, []bundle.FileInputSnapshot{input}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(w.BundleDir, "helpers")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(w.BundleDir, "helpers")); err != nil {
		t.Skipf("create workspace symlink: %v", err)
	}
	if _, err := w.syncSourceWithInputs(source, []bundle.FileInputSnapshot{input}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(filepath.Join(w.BundleDir, "helpers")); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("input ancestor was not restored as a real directory: info=%v err=%v", info, err)
	}
	if got, err := os.ReadFile(filepath.Join(w.BundleDir, "helpers", "theme.py")); err != nil || string(got) != "orange\n" {
		t.Fatalf("composed input = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "theme.py")); !os.IsNotExist(err) {
		t.Fatalf("input escaped workspace through symlink ancestor: %v", err)
	}
}

func TestWorkspaceFailedSyncDiscardsUnindexedWrites(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "copied-before-error.py"), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := workspaceFor(source, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	invalid := bundle.FileInputSnapshot{From: "_shared/bad.py", To: "../unsafe.py", Mode: 0o644, Data: []byte("bad\n")}
	if _, err := w.syncSourceWithInputs(source, []bundle.FileInputSnapshot{invalid}); err == nil {
		t.Fatal("sync unexpectedly accepted an unsafe post-copy input")
	}
	entries, err := os.ReadDir(w.BundleDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed sync left unindexed workspace entries: %v", entries)
	}
	if _, err := os.Stat(w.indexPath); !os.IsNotExist(err) {
		t.Fatalf("failed sync left a source index: %v", err)
	}
}

func TestWorkspaceComposedDependencyInputInvalidatesPreparation(t *testing.T) {
	source := t.TempDir()
	w, err := workspaceFor(source, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	input := bundle.FileInputSnapshot{From: "_shared/requirements.txt", To: "requirements.txt", Mode: 0o644, Data: []byte("shiny\n")}
	changed, err := w.syncSourceWithInputs(source, []bundle.FileInputSnapshot{input})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first composed dependency input was not marked changed")
	}
	if err := w.markDependenciesReady(); err != nil {
		t.Fatal(err)
	}
	changed, err = w.syncSourceWithInputs(source, []bundle.FileInputSnapshot{input})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("unchanged composed dependency input invalidated preparation")
	}
	input.Data = []byte("shiny\nplotly\n")
	changed, err = w.syncSourceWithInputs(source, []bundle.FileInputSnapshot{input})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("edited composed dependency input did not invalidate preparation")
	}
}

func TestFleetWorkspaceIdentitySeparatesSlugsAndOrdinaryRun(t *testing.T) {
	source := t.TempDir()
	manifest := filepath.Join(t.TempDir(), "fleet.toml")
	ordinary, err := workspaceForIdentity(source, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	sales, err := workspaceForIdentity(source, "", "", manifest+"\x00sales")
	if err != nil {
		t.Fatal(err)
	}
	finance, err := workspaceForIdentity(source, "", "", manifest+"\x00finance")
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.Root == sales.Root || sales.Root == finance.Root || ordinary.Root == finance.Root {
		t.Fatalf("identity roots must be distinct: ordinary=%s sales=%s finance=%s", ordinary.Root, sales.Root, finance.Root)
	}
	if ordinary.DataDir == sales.DataDir || sales.DataDir == finance.DataDir {
		t.Fatalf("default data dirs must follow distinct identities: ordinary=%s sales=%s finance=%s", ordinary.DataDir, sales.DataDir, finance.DataDir)
	}

	explicitState := t.TempDir()
	first, err := workspaceForIdentity(source, explicitState, "", manifest+"\x00sales")
	if err != nil {
		t.Fatal(err)
	}
	second, err := workspaceForIdentity(source, explicitState, "", manifest+"\x00finance")
	if err != nil {
		t.Fatal(err)
	}
	release, err := first.acquireLock()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := second.acquireLock(); err == nil || !strings.Contains(err.Error(), "this local workspace already has a runner") {
		t.Fatalf("same explicit workspace contention error = %v", err)
	}
}

func TestWorkspaceResetPreservesData(t *testing.T) {
	source := t.TempDir()
	w, err := workspaceFor(source, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.syncSource(source); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.BundleDir, "generated"), []byte("state"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.DataDir, "durable"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.resetBundle(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.BundleDir, "generated")); !os.IsNotExist(err) {
		t.Fatalf("generated state survived reset: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(w.DataDir, "durable")); err != nil || string(got) != "data" {
		t.Fatalf("durable data was not preserved: %q, %v", got, err)
	}
}

package localrun

import (
	"os"
	"path/filepath"
	"testing"
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

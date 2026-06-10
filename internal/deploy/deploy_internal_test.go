package deploy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveBootParams_ManifestCommandBeforeTypeDetection(t *testing.T) {
	// Bundle with NEITHER app.py nor app.R but a valid manifest command must
	// resolve (the capability this feature unlocks). Same bundle without
	// the command must still error.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ManifestFilename), []byte("[app]\ncommand = [\"serve\", \"--port\", \"{port}\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	baseCmd, appType, autoInstr, _, _, err := resolveBootParams(Params{Slug: "x", BundleDir: dir}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(baseCmd) != 3 || baseCmd[2] != "{port}" {
		t.Fatalf("baseCmd = %v; must be the UNSUBSTITUTED template", baseCmd)
	}
	if appType != "" || autoInstr {
		t.Fatalf("type detection and auto-instrument must be skipped; got %q %v", appType, autoInstr)
	}
	empty := t.TempDir()
	if _, _, _, _, _, err := resolveBootParams(Params{Slug: "x", BundleDir: empty}, true); err == nil {
		t.Fatal("no app.py, no app.R, no command must still error")
	}
}

func TestResolveBootParams_UnparseableManifestIsFatal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ManifestFilename), []byte("not [valid toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, err := resolveBootParams(Params{Slug: "x", BundleDir: dir}, true); err == nil {
		t.Fatal("unparseable manifest must fail the boot, not silently fall back")
	}
}

func TestResolveBootParams_InferredPathUnchangedWithoutCommand(t *testing.T) {
	// app.py present, no manifest: type detection still runs.
	// (hostDeps=false to skip uv sync)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("# app"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, appType, _, _, _, err := resolveBootParams(Params{Slug: "x", BundleDir: dir}, false)
	if err != nil {
		t.Fatal(err)
	}
	if appType != "python" {
		t.Fatalf("appType = %q", appType)
	}
}

func TestResolveBootParams_CommandVersionsWithBundle(t *testing.T) {
	v1, v2 := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(v1, ManifestFilename), []byte("[app]\ncommand = [\"serve-v1\", \"{port}\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v2, ManifestFilename), []byte("[app]\ncommand = [\"serve-v2\", \"{port}\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c1, _, _, _, _, err := resolveBootParams(Params{Slug: "x", BundleDir: v1}, true)
	if err != nil {
		t.Fatal(err)
	}
	c2, _, _, _, _, err := resolveBootParams(Params{Slug: "x", BundleDir: v2}, true)
	if err != nil {
		t.Fatal(err)
	}
	if c1[0] != "serve-v1" || c2[0] != "serve-v2" {
		t.Fatalf("rollback must boot the rolled-back bundle's command: %v / %v", c1, c2)
	}
}

package upgrade

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeExecutable creates a runnable file and returns its absolute path.
func writeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveReexecTarget_AbsolutePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script executables are not a thing on windows")
	}
	bin := writeExecutable(t, t.TempDir(), "shinyhub")
	got, err := ResolveReexecTarget(bin)
	if err != nil {
		t.Fatalf("absolute executable path must resolve: %v", err)
	}
	if got != bin {
		t.Fatalf("resolved %q, want %q", got, bin)
	}
}

func TestResolveReexecTarget_AbsolutePathMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "shinyhub")
	if _, err := ResolveReexecTarget(missing); err == nil {
		t.Fatal("nonexistent absolute path must not resolve")
	}
}

// A bare argv[0] (how the pre-fix PyPI wheel launched the binary) resolves only
// through PATH: absent there it must fail, present there it must succeed. Both
// polarities run against the same name so a broken lookup cannot pass as "not
// found".
func TestResolveReexecTarget_BareNameUsesPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script executables are not a thing on windows")
	}
	empty := t.TempDir()
	binDir := t.TempDir()
	writeExecutable(t, binDir, "shinyhub")

	t.Setenv("PATH", empty)
	if _, err := ResolveReexecTarget("shinyhub"); err == nil {
		t.Fatal("bare name with no match on PATH must not resolve")
	}

	t.Setenv("PATH", binDir)
	got, err := ResolveReexecTarget("shinyhub")
	if err != nil {
		t.Fatalf("bare name present on PATH must resolve: %v", err)
	}
	if want := filepath.Join(binDir, "shinyhub"); got != want {
		t.Fatalf("resolved %q, want %q", got, want)
	}
}

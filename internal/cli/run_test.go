package cli

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRunCmd_FlagsAndNoLogin(t *testing.T) {
	cmd := newRunCmd()
	if cmd.Use[:3] != "run" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	for _, f := range []string{"port", "no-sync", "no-reload", "env", "env-file", "data-dir", "slug", "open", "check"} {
		if cmd.Flags().Lookup(f) == nil {
			t.Errorf("missing flag --%s", f)
		}
	}
}

func TestReadRunEnvFile_ExportPrefix(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, ".env")
	if err := os.WriteFile(f, []byte(
		"export FOO=bar\nexport BAZ=qux\n# comment\n\nNORMAL=yes\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readRunEnvFile(f)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"FOO=bar", "BAZ=qux", "NORMAL=yes"}
	if !slices.Equal(got, want) {
		t.Fatalf("readRunEnvFile = %v, want %v", got, want)
	}
}

func TestReadRunEnvFile_SkipsNoEquals(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, ".env")
	if err := os.WriteFile(f, []byte(
		"GOOD=value\nBADNOEQUALS\nexport ALSOBAD\nANOTHER=ok\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readRunEnvFile(f)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"GOOD=value", "ANOTHER=ok"}
	if !slices.Equal(got, want) {
		t.Fatalf("readRunEnvFile = %v, want %v", got, want)
	}
}

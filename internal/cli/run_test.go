package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRunCmd_FlagsAndNoLogin(t *testing.T) {
	cmd := newRunCmd()
	if cmd.Use[:3] != "run" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	for _, f := range []string{"port", "no-sync", "no-reload", "fresh", "env", "env-file", "data-dir", "state-dir", "slug", "open", "check"} {
		if cmd.Flags().Lookup(f) == nil {
			t.Errorf("missing flag --%s", f)
		}
	}
}

func TestRunCmd_HelpOmitsIrrelevantServerFlags(t *testing.T) {
	parent := &cobra.Command{Use: "shinyhub"}
	AddCommandsTo(parent)
	run, _, err := parent.Find([]string{"run"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	run.SetOut(&out)
	if err := run.Help(); err != nil {
		t.Fatal(err)
	}
	help := out.String()
	if !strings.Contains(help, "--fresh") {
		t.Fatalf("run help omitted local flags:\n%s", help)
	}
	if strings.Contains(help, "Global Flags:") || strings.Contains(help, "--host") {
		t.Fatalf("run help advertises irrelevant server flags:\n%s", help)
	}
}

func TestRunCmd_RejectsIrrelevantServerFlags(t *testing.T) {
	parent := &cobra.Command{Use: "shinyhub"}
	AddCommandsTo(parent)
	parent.SetArgs([]string{"run", "--host", "https://hub.example.com"})
	parent.SetOut(io.Discard)
	parent.SetErr(io.Discard)
	err := parent.Execute()
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Kind != KindValidation || !strings.Contains(err.Error(), "does not apply") {
		t.Fatalf("error = %v, want actionable validation error", err)
	}
	hostFlagOverride = ""
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
	want := []envFileEntry{{Key: "FOO", Value: "bar"}, {Key: "BAZ", Value: "qux"}, {Key: "NORMAL", Value: "yes"}}
	if !slices.Equal(got, want) {
		t.Fatalf("readRunEnvFile = %v, want %v", got, want)
	}
}

func TestReadRunEnvFile_RejectsNoEquals(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, ".env")
	if err := os.WriteFile(f, []byte(
		"GOOD=value\nBADNOEQUALS\nexport ALSOBAD\nANOTHER=ok\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readRunEnvFile(f); err == nil {
		t.Fatal("malformed env file must fail instead of silently dropping lines")
	}
}

func TestReadRunEnvFile_UsesDeployQuotingSemantics(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, ".env")
	if err := os.WriteFile(f, []byte("QUOTED=\"value with spaces\"\nCOMMENTED=value # note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readRunEnvFile(f)
	if err != nil {
		t.Fatal(err)
	}
	want := []envFileEntry{{Key: "QUOTED", Value: "value with spaces"}, {Key: "COMMENTED", Value: "value"}}
	if !slices.Equal(got, want) {
		t.Fatalf("readRunEnvFile = %#v, want %#v", got, want)
	}
}

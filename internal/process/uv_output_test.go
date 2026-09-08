package process

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// colouredFailure is what a build backend writes when it colours its output
// unconditionally: a red "error", a bold line, and an OSC hyperlink.
const colouredFailure = "\x1b[31merror\x1b[0m: \x1b[1mno matching distribution\x1b[0m\n" +
	"\x1b]8;;https://example.invalid/help\x07docs\x1b]8;;\x07\n"

const colouredFailurePlain = "error: no matching distribution\ndocs\n"

func TestUVBuildOutput_StripsEscapesAndKeepsTheText(t *testing.T) {
	got := string(uvBuildOutput([]byte(colouredFailure)))
	if got != colouredFailurePlain {
		t.Errorf("uvBuildOutput() = %q, want %q", got, colouredFailurePlain)
	}
}

// TestUVBuildOutput_LeavesPlainTextAlone is the negative control: a stripper
// that deleted too much would still satisfy the test above.
func TestUVBuildOutput_LeavesPlainTextAlone(t *testing.T) {
	plain := []byte("Resolved 12 packages in 340ms\nerror: build failed\n")
	if got := uvBuildOutput(plain); !bytes.Equal(got, plain) {
		t.Errorf("uvBuildOutput() altered plain text: %q -> %q", plain, got)
	}
}

// fakeUV puts a `uv` on PATH that writes script to stdout and exits 1, so the
// build-failure path can be driven without a real uv or a network.
func fakeUV(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake uv is a shell script")
	}
	bin := t.TempDir()
	body := "#!/bin/sh\nprintf '%s' " + shellQuote(script) + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "uv"), []byte(body), 0o755); err != nil {
		t.Fatalf("write fake uv: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// TestSync_BuildFailureCarriesNoEscapes drives the real Sync path. The error it
// returns is rendered into a JSON deploy response and then into a terminal or a
// browser log pane, so a raw escape from the app's own build backend arrives as
// mojibake at best and as a cursor-moving control sequence at worst.
func TestSync_BuildFailureCarriesNoEscapes(t *testing.T) {
	fakeUV(t, colouredFailure)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname = \"x\"\n"), 0o644); err != nil {
		t.Fatalf("write pyproject.toml: %v", err)
	}

	err := Sync(context.Background(), dir)
	if err == nil {
		t.Fatal("expected the fake uv's non-zero exit to surface as an error")
	}
	msg := err.Error()
	// Positive control: the fake did emit output and it reached the error, so a
	// clean assertion below cannot pass by the output having gone missing.
	if !strings.Contains(msg, "no matching distribution") {
		t.Fatalf("build output missing from the error: %q", msg)
	}
	if strings.ContainsRune(msg, '\x1b') {
		t.Errorf("error carries raw ANSI escapes: %q", msg)
	}
}

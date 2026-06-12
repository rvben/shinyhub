// cmd/shinyhub/worker_test.go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerCommandRegistered(t *testing.T) {
	var found bool
	for _, c := range buildRoot().Commands() {
		if c.Name() == "worker" {
			found = true
			// Required flags must exist.
			for _, f := range []string{"server", "token", "advertise-addr", "tier", "data-dir"} {
				if c.Flags().Lookup(f) == nil {
					t.Errorf("worker command missing --%s flag", f)
				}
			}
		}
	}
	if !found {
		t.Fatal("worker subcommand not registered on rootCmd")
	}
}

func TestWorkerCommandRequiresServerAndToken(t *testing.T) {
	cmd := newWorkerCmd()
	cmd.SetArgs([]string{"--tier", "burst", "--advertise-addr", "127.0.0.1:1", "--data-dir", t.TempDir()})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "server") {
		t.Fatalf("expected missing --server error, got %v", err)
	}
}

func TestReadJoinTokensEmptyPath(t *testing.T) {
	_, err := readJoinTokens("")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestReadJoinTokensMissingFile(t *testing.T) {
	_, err := readJoinTokens(filepath.Join(t.TempDir(), "no-such-file"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "read token file") {
		t.Fatalf("error should mention path context, got: %v", err)
	}
}

func TestReadJoinTokensParsesTokens(t *testing.T) {
	f := filepath.Join(t.TempDir(), "tokens")
	if err := os.WriteFile(f, []byte("  alpha  \nbeta\n\n  \ngamma\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	tokens, err := readJoinTokens(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"alpha", "beta", "gamma"}
	if len(tokens) != len(want) {
		t.Fatalf("got %v, want %v", tokens, want)
	}
	for i, w := range want {
		if tokens[i] != w {
			t.Fatalf("token[%d] = %q, want %q", i, tokens[i], w)
		}
	}
}

func TestReadJoinTokensAllBlank(t *testing.T) {
	f := filepath.Join(t.TempDir(), "tokens")
	if err := os.WriteFile(f, []byte("\n  \n\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := readJoinTokens(f)
	if err == nil || !strings.Contains(err.Error(), "no join tokens") {
		t.Fatalf("expected 'no join tokens' error, got: %v", err)
	}
}

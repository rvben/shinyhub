package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/cli"
)

func TestSupervisedServeError(t *testing.T) {
	for _, tc := range []struct {
		name, script, kind string
		code               int
	}{
		{"schema refusal", "exit 7", "schema_incompatible", 7},
		{"signal death", "kill -KILL $$", "internal", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := exec.Command("sh", "-c", tc.script).Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("child error = %v, want ExitError", err)
			}
			path := filepath.Join(t.TempDir(), "stderr.json")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			previous := os.Stderr
			os.Stderr = file
			t.Cleanup(func() { os.Stderr = previous; _ = file.Close() })
			if code := cli.Report(supervisedServeError(exit)); code != tc.code {
				t.Fatalf("process exit = %d, want %d", code, tc.code)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var envelope struct {
				Error struct {
					Kind string `json:"kind"`
				} `json:"error"`
			}
			if err := json.Unmarshal(data, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Kind != tc.kind {
				t.Fatalf("error kind = %q, want %q", envelope.Error.Kind, tc.kind)
			}
		})
	}
	plain := errors.New("supervisor publication failed")
	if supervisedServeError(plain) != plain || supervisedServeError(nil) != nil {
		t.Fatal("non-child errors must pass through unchanged")
	}
}

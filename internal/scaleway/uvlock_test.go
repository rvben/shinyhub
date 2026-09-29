package scaleway

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

const lockedLock = `version = 1
revision = 1

[[package]]
name = "app"
version = "0.1.0"
source = { virtual = "." }
dependencies = [
    { name = "six" },
]

[package.metadata]
requires-dist = [{ name = "six" }]

[[package]]
name = "six"
version = "1.17.0"
source = { registry = "https://mirror.example/simple" }
`

func lockedBundle(t *testing.T, deps string) string {
	t.Helper()
	dir := t.TempDir()
	pyproject := "[project]\nname = \"app\"\nversion = \"0.1.0\"\ndependencies = [" + deps + "]\n"
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(pyproject), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "uv.lock"), []byte(lockedLock), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The runner image cannot check a uv.lock against pyproject.toml, so the
// container the control plane creates tells it to resolve a stale lock again
// and says nothing for a current one, which the runner installs frozen.
func TestStartTellsRunnerToResolveStaleLock(t *testing.T) {
	cases := []struct {
		name string
		dir  string
		want string
	}{
		{"stale lock", lockedBundle(t, `"six", "idna"`), process.UVLockModeResolve},
		{"current lock", lockedBundle(t, `"six"`), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var created CreateContainerInput
			client := &fakeClient{createFn: func(_ context.Context, in CreateContainerInput) (Container, error) {
				created = in
				return Container{ID: "container-id", Name: in.Name, Status: StatusReady,
					PublicEndpoint: "https://ops.functions.fnc.nl-ams.scw.cloud", Tags: in.Tags}, nil
			}}
			rt, err := New(client, testConfig(), nil, WithPollInterval(time.Millisecond))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			p := testStartParams()
			p.Dir = tc.dir
			if _, err := rt.Start(context.Background(), p, io.Discard); err != nil {
				t.Fatalf("Start: %v", err)
			}
			got, set := created.Environment[process.UVLockModeEnv]
			if tc.want == "" {
				if set {
					t.Errorf("%s = %q, want it unset", process.UVLockModeEnv, got)
				}
				return
			}
			if got != tc.want {
				t.Errorf("%s = %q, want %q", process.UVLockModeEnv, got, tc.want)
			}
			if _, secret := created.SecretEnvironment[process.UVLockModeEnv]; secret {
				t.Errorf("%s is sent as a secret", process.UVLockModeEnv)
			}
		})
	}
}

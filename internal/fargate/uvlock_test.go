package fargate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

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

// The runner image cannot check a uv.lock against pyproject.toml, so the task
// override tells it to resolve a stale lock again and says nothing for a
// current one, which the runner installs frozen.
func TestContainerOverrideTellsRunnerToResolveStaleLock(t *testing.T) {
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
			r := New(&fakeECS{}, testCfg(), nil)
			ov := r.buildContainerOverride(process.StartParams{Slug: "demo", Dir: tc.dir})
			var values []string
			for _, kv := range ov.Environment {
				if aws.ToString(kv.Name) == process.UVLockModeEnv {
					values = append(values, aws.ToString(kv.Value))
				}
			}
			switch {
			case tc.want == "" && len(values) > 0:
				t.Errorf("%s = %q, want it unset", process.UVLockModeEnv, values)
			case tc.want != "" && (len(values) != 1 || values[0] != tc.want):
				t.Errorf("%s = %q, want exactly [%q]", process.UVLockModeEnv, values, tc.want)
			}
		})
	}
}

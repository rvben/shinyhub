package process_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/process"
)

// currentPyproject and currentLock are a matched pair in the shape uv writes:
// the lock was produced by `uv lock` from this pyproject.toml. The lock spells
// names in PEP 503 form ("My_App" -> "my-app", "Six" -> "six"), folds optional
// dependencies into requires-dist under an `extra ==` marker, and rewrites
// markers (`python_version` -> `python_full_version`), so only the names line up
// with the declared requirement strings.
const currentPyproject = `[project]
name = "My_App"
version = "0.1.0"
requires-python = ">=3.9"
dependencies = ["Six >= 1.16", "idna[all]; python_version >= '3.9'", "typing_extensions"]

[project.optional-dependencies]
extra1 = ["certifi"]

[dependency-groups]
dev = ["iniconfig"]
`

const currentLock = `version = 1
revision = 1
requires-python = ">=3.9"

[[package]]
name = "certifi"
version = "2025.1.31"
source = { registry = "https://mirror.example/simple" }

[[package]]
name = "idna"
version = "3.10"
source = { registry = "https://mirror.example/simple" }

[[package]]
name = "my-app"
version = "0.1.0"
source = { virtual = "." }
dependencies = [
    { name = "idna", extra = ["all"] },
    { name = "six" },
    { name = "typing-extensions" },
]

[package.optional-dependencies]
extra1 = [
    { name = "certifi" },
]

[package.dev-dependencies]
dev = [
    { name = "iniconfig" },
]

[package.metadata]
requires-dist = [
    { name = "certifi", marker = "extra == 'extra1'" },
    { name = "idna", extras = ["all"], marker = "python_full_version >= '3.9'" },
    { name = "six", specifier = ">=1.16" },
    { name = "typing-extensions" },
]
provides-extras = ["extra1"]

[package.metadata.requires-dev]
dev = [{ name = "iniconfig" }]

[[package]]
name = "six"
version = "1.17.0"
source = { registry = "https://mirror.example/simple" }
`

// emptyLock is what uv writes for a project without requirements: the root
// package carries no [package.metadata] at all.
const emptyLock = `version = 1
revision = 1
requires-python = ">=3.9"

[[package]]
name = "b"
version = "0.1.0"
source = { virtual = "." }
`

// groupsPyproject and groupsLock are a matched pair produced by `uv lock`:
// uv expands an included group into the including one and folds legacy
// [tool.uv] dev-dependencies into the "dev" group, normalizing group names.
const groupsPyproject = `[project]
name = "g"
version = "0.1.0"
requires-python = ">=3.12"
dependencies = ["six"]

[dependency-groups]
dev = ["iniconfig"]
Lint_Group = ["packaging", {include-group = "dev"}]

[tool.uv]
package = false
dev-dependencies = ["pluggy"]
`

const groupsLock = `version = 1
revision = 3
requires-python = ">=3.12"

[[package]]
name = "g"
version = "0.1.0"
source = { virtual = "." }
dependencies = [
    { name = "six" },
]

[package.dev-dependencies]
dev = [
    { name = "iniconfig" },
    { name = "pluggy" },
]
lint-group = [
    { name = "iniconfig" },
    { name = "packaging" },
]

[package.metadata]
requires-dist = [{ name = "six" }]

[package.metadata.requires-dev]
dev = [
    { name = "iniconfig" },
    { name = "pluggy" },
]
lint-group = [
    { name = "iniconfig" },
    { name = "packaging" },
]
`

func writeProject(t *testing.T, pyproject, lock string) string {
	t.Helper()
	dir := t.TempDir()
	if pyproject != "" {
		if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(pyproject), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if lock != "" {
		if err := os.WriteFile(filepath.Join(dir, "uv.lock"), []byte(lock), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLockSyncFlags(t *testing.T) {
	frozen := []string{"--frozen"}
	cases := []struct {
		name      string
		pyproject string
		lock      string
		want      []string
		stale     []string // substrings the stale-lock error must contain
	}{
		{
			name:      "no lock keeps a plain sync, which creates one",
			pyproject: currentPyproject,
			want:      nil,
		},
		{
			name:      "current lock installs frozen",
			pyproject: currentPyproject,
			lock:      currentLock,
			want:      frozen,
		},
		{
			name: "declared requirement missing from the lock is stale",
			pyproject: strings.Replace(currentPyproject,
				`"typing_extensions"]`, `"typing_extensions", "Requests"]`, 1),
			lock:  currentLock,
			stale: []string{"pyproject.toml declares requests, which uv.lock does not record", "Run `uv lock`"},
		},
		{
			name: "optional requirement missing from the lock is stale",
			pyproject: strings.Replace(currentPyproject,
				`extra1 = ["certifi"]`, `extra1 = ["certifi", "urllib3"]`, 1),
			lock:  currentLock,
			stale: []string{"pyproject.toml declares urllib3 (extra extra1)"},
		},
		{
			name: "locked requirement no longer declared is stale",
			pyproject: strings.Replace(currentPyproject,
				`"Six >= 1.16", `, ``, 1),
			lock:  currentLock,
			stale: []string{"uv.lock records six, which pyproject.toml no longer declares"},
		},
		{
			name: "a changed specifier alone is not judged",
			pyproject: strings.Replace(currentPyproject,
				`"Six >= 1.16"`, `"six>=1.17,<2"`, 1),
			lock: currentLock,
			want: frozen,
		},
		{
			name: "requirement moved from an extra into the dependencies is stale",
			pyproject: strings.Replace(strings.Replace(currentPyproject,
				`"typing_extensions"]`, `"typing_extensions", "certifi"]`, 1),
				`extra1 = ["certifi"]`, `extra1 = []`, 1),
			lock: currentLock,
			stale: []string{"pyproject.toml declares certifi, which uv.lock does not record",
				"uv.lock records certifi (extra extra1), which pyproject.toml no longer declares"},
		},
		{
			name: "requirement moved to a different extra is stale",
			pyproject: strings.Replace(currentPyproject,
				`extra1 = ["certifi"]`, `Extra_2 = ["certifi"]`, 1),
			lock:  currentLock,
			stale: []string{"pyproject.toml declares certifi (extra extra-2)"},
		},
		{
			name: "requirement added to a dependency group is stale",
			pyproject: strings.Replace(currentPyproject,
				`dev = ["iniconfig"]`, `dev = ["iniconfig", "pytest"]`, 1),
			lock:  currentLock,
			stale: []string{"pyproject.toml declares pytest (group dev)"},
		},
		{
			name: "dependency group removed from pyproject.toml is stale",
			pyproject: strings.Replace(currentPyproject,
				"[dependency-groups]\ndev = [\"iniconfig\"]\n", "", 1),
			lock:  currentLock,
			stale: []string{"uv.lock records iniconfig (group dev)"},
		},
		{
			name:      "included groups and legacy dev-dependencies match the lock uv writes",
			pyproject: groupsPyproject,
			lock:      groupsLock,
			want:      frozen,
		},
		{
			name: "requirement added through an included group is stale",
			pyproject: strings.Replace(groupsPyproject,
				`dev = ["iniconfig"]`, `dev = ["iniconfig", "pytest"]`, 1),
			lock:  groupsLock,
			stale: []string{"pytest (group dev)", "pytest (group lint-group)"},
		},
		{
			name: "a group entry it cannot resolve leaves groups to uv but still checks dependencies",
			pyproject: strings.Replace(strings.Replace(groupsPyproject,
				`{include-group = "dev"}`, `{include-group = "missing"}`, 1),
				`dependencies = ["six"]`, `dependencies = ["six", "idna"]`, 1),
			lock:  groupsLock,
			stale: []string{"pyproject.toml declares idna, which uv.lock does not record"},
		},
		{
			name: "an unresolvable group entry alone is left to uv",
			pyproject: strings.Replace(groupsPyproject,
				`{include-group = "dev"}`, `{include-group = "missing"}`, 1),
			lock: groupsLock,
			want: frozen,
		},
		{
			name:      "a lock from before uv recorded requires-dev leaves groups to uv",
			pyproject: strings.Replace(currentPyproject, `dev = ["iniconfig"]`, `dev = ["iniconfig", "pytest"]`, 1),
			lock:      strings.Replace(currentLock, "[package.metadata.requires-dev]\ndev = [{ name = \"iniconfig\" }]\n", "", 1),
			want:      frozen,
		},
		{
			name: "an extra marker it cannot parse is left to uv",
			pyproject: strings.Replace(currentPyproject,
				`"typing_extensions"]`, `"typing_extensions", "requests"]`, 1),
			lock: strings.Replace(currentLock,
				`marker = "extra == 'extra1'"`, `marker = "extra == 'extra1' or extra == 'x'"`, 1),
			want: frozen,
		},
		{
			name: "requesting an extra of a dependency is stale",
			pyproject: strings.Replace(currentPyproject,
				`"typing_extensions"]`, `"typing_extensions[fast]"]`, 1),
			lock: currentLock,
			stale: []string{"pyproject.toml declares typing-extensions[fast]",
				"uv.lock records typing-extensions, which"},
		},
		{
			name: "dropping a requested extra is stale",
			pyproject: strings.Replace(currentPyproject,
				`"idna[all]; python_version >= '3.9'"`, `"idna; python_version >= '3.9'"`, 1),
			lock:  currentLock,
			stale: []string{"uv.lock records idna[all]"},
		},
		{
			name: "extras are compared regardless of case, spacing and order",
			pyproject: strings.Replace(currentPyproject,
				`"idna[all]; python_version >= '3.9'"`, `"idna [ B_Extra , all ]; python_version >= '3.9'"`, 1),
			lock: strings.Replace(currentLock,
				`extras = ["all"]`, `extras = ["all", "b-extra"]`, 1),
			want: frozen,
		},
		{
			name:      "project without requirements matches a lock without metadata",
			pyproject: "[project]\nname = \"b\"\nversion = \"0.1.0\"\ndependencies = []\n",
			lock:      emptyLock,
			want:      frozen,
		},
		{
			name:      "requirement added to a project locked while empty is stale",
			pyproject: "[project]\nname = \"b\"\nversion = \"0.1.0\"\ndependencies = [\"six\"]\n",
			lock:      emptyLock,
			stale:     []string{"pyproject.toml declares six"},
		},
		{
			name:      "root without metadata but with dependencies is left to uv",
			pyproject: "[project]\nname = \"b\"\nversion = \"0.1.0\"\ndependencies = [\"six\"]\n",
			lock:      emptyLock + "dependencies = [\n    { name = \"idna\" },\n]\n",
			want:      frozen,
		},
		{
			name: "dynamic dependencies are left to uv",
			pyproject: strings.Replace(currentPyproject,
				`version = "0.1.0"`, `version = "0.1.0"`+"\ndynamic = [\"dependencies\"]", 1),
			lock: currentLock,
			want: frozen,
		},
		{
			name:      "a lock whose root package is not found is left to uv",
			pyproject: strings.Replace(currentPyproject, `name = "My_App"`, `name = "other"`, 1),
			lock:      currentLock,
			want:      frozen,
		},
		{
			name:      "malformed pyproject.toml is left to uv",
			pyproject: "[project\nname = ",
			lock:      currentLock,
			want:      frozen,
		},
		{
			name:      "unparseable requirement is left to uv",
			pyproject: strings.Replace(currentPyproject, `"typing_extensions"]`, `"typing_extensions", "@bad"]`, 1),
			lock:      currentLock,
			want:      frozen,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeProject(t, tc.pyproject, tc.lock)
			got := process.LockSyncFlags(dir)
			err := process.CheckLockCurrent(dir)
			if len(tc.stale) > 0 {
				if !errors.Is(err, process.ErrStaleLock) {
					t.Fatalf("err = %v, want ErrStaleLock", err)
				}
				for _, want := range tc.stale {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q lacks %q", err, want)
					}
				}
				// A stale lock that reaches a build is re-resolved, not
				// installed frozen without what pyproject.toml declares.
				if got != nil {
					t.Errorf("flags = %q for a stale lock, want a plain sync", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("CheckLockCurrent: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("flags = %q, want %q", got, tc.want)
			}
		})
	}
}

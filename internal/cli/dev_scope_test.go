package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeDevFleetFixture(t *testing.T) (root, salesDir, opsDir string) {
	t.Helper()
	root = t.TempDir()
	salesDir = filepath.Join(root, "apps", "sales")
	opsDir = filepath.Join(root, "apps", "ops")
	for _, dir := range []string{salesDir, opsDir, filepath.Join(root, "shared")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, body := range map[string]string{
		filepath.Join(salesDir, "app.py"):         "# sales\n",
		filepath.Join(opsDir, "app.py"):           "# ops\n",
		filepath.Join(root, "shared", "theme.py"): "orange\n",
		filepath.Join(root, "fleet.toml"): `fleet_id = "analytics"
[[bundle_file]]
from = "shared/theme.py"
to = "helpers/theme.py"
consumers = ["sales"]
[[app]]
slug = "sales"
source = "apps/sales"
visibility = "shared"
[[app]]
slug = "ops"
source = "apps/ops"
[[app]]
slug = "warehouse"
source = "git+https://example.test/apps.git@main#warehouse"
`,
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, salesDir, opsDir
}

func TestResolveDevScopeIsContextAware(t *testing.T) {
	root, salesDir, _ := writeDevFleetFixture(t)

	t.Run("fleet root selects every watchable local app", func(t *testing.T) {
		scope, err := resolveDevScope(root, "fleet.toml", false, false, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !scope.fleet() || scope.FleetID != "analytics" {
			t.Fatalf("scope = %+v", scope)
		}
		if got := devTargetSlugs(scope.Targets); !reflect.DeepEqual(got, []string{"sales", "ops"}) {
			t.Fatalf("targets = %v", got)
		}
		if !reflect.DeepEqual(scope.SkippedGit, []string{"warehouse"}) {
			t.Fatalf("skipped git apps = %v", scope.SkippedGit)
		}
	})

	t.Run("nested app inherits manifest identity and bundle inputs", func(t *testing.T) {
		scope, err := resolveDevScope(salesDir, "fleet.toml", false, false, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := devTargetSlugs(scope.Targets); !reflect.DeepEqual(got, []string{"sales"}) {
			t.Fatalf("targets = %v", got)
		}
		target := scope.Targets[0]
		if target.Visibility != "shared" || len(target.BundleInputs) != 1 || target.BundleInputs[0].To != "helpers/theme.py" {
			t.Fatalf("target = %+v", target)
		}
		if len(target.ExternalFiles) != 1 || !strings.HasSuffix(target.ExternalFiles[0], filepath.Join("shared", "theme.py")) {
			t.Fatalf("external files = %v", target.ExternalFiles)
		}
	})

	t.Run("explicit app selection", func(t *testing.T) {
		scope, err := resolveDevScope(root, "fleet.toml", false, false, false, []string{"ops", "ops"})
		if err != nil {
			t.Fatal(err)
		}
		if got := devTargetSlugs(scope.Targets); !reflect.DeepEqual(got, []string{"ops"}) {
			t.Fatalf("targets = %v", got)
		}
	})

	t.Run("standalone is an explicit escape hatch", func(t *testing.T) {
		scope, err := resolveDevScope(salesDir, "fleet.toml", false, true, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if scope.fleet() {
			t.Fatalf("standalone scope discovered fleet: %+v", scope)
		}
	})

	t.Run("explicit all refuses an unwatchable git source", func(t *testing.T) {
		_, err := resolveDevScope(root, "fleet.toml", false, false, true, nil)
		if err == nil || !strings.Contains(err.Error(), "git+ source") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestResolveDevScopeRejectsAmbiguousAndInvalidSelectors(t *testing.T) {
	root, salesDir, _ := writeDevFleetFixture(t)
	for _, tc := range []struct {
		name       string
		standalone bool
		all        bool
		apps       []string
		want       string
	}{
		{name: "all plus app", all: true, apps: []string{"sales"}, want: "mutually exclusive"},
		{name: "empty app", apps: []string{""}, want: "non-empty"},
		{name: "unknown app", apps: []string{"missing"}, want: "not declared"},
		{name: "standalone plus app", standalone: true, apps: []string{"sales"}, want: "cannot be combined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveDevScope(root, "fleet.toml", false, tc.standalone, tc.all, tc.apps)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
	_, err := resolveDevScope(salesDir, "fleet.toml", false, false, false, []string{"ops"})
	if err == nil || !strings.Contains(err.Error(), "directory is fleet app sales") || !strings.Contains(hintOf(err), "fleet root") {
		t.Fatalf("mismatched path and --app error = %v, hint = %q", err, hintOf(err))
	}
	_, err = resolveDevScope(filepath.Join(root, "fleet.toml"), "fleet.toml", false, true, false, nil)
	if err == nil || !strings.Contains(err.Error(), "app directory") {
		t.Fatalf("standalone manifest error = %v", err)
	}
}

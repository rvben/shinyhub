package fleet

import (
	"strings"
	"testing"
)

func TestParseManifest_Valid(t *testing.T) {
	src := `
fleet_id = "prod-eu"

[[app]]
slug = "alpha"
source = "git+https://example.com/alpha.git"
visibility = "public"

[[app]]
slug = "beta"
source = "git+https://example.com/beta.git"

  [app.config]
  replicas = 2
  hibernate_timeout_minutes = 30
`
	m, probs := ParseManifest([]byte(src), "shinyhub-fleet.toml")
	if len(probs) != 0 {
		t.Fatalf("unexpected problems: %v", probs)
	}
	if m.FleetID != "prod-eu" {
		t.Fatalf("FleetID = %q", m.FleetID)
	}
	if len(m.Apps) != 2 {
		t.Fatalf("len(Apps) = %d, want 2", len(m.Apps))
	}
	if m.Apps[0].Slug != "alpha" || m.Apps[0].Visibility != "public" {
		t.Fatalf("app[0] = %+v", m.Apps[0])
	}
	if m.Apps[1].Visibility != "private" {
		t.Fatalf("app[1] default visibility = %q, want private", m.Apps[1].Visibility)
	}
	if m.Apps[1].Config.Replicas == nil || *m.Apps[1].Config.Replicas != 2 {
		t.Fatalf("app[1].Config.Replicas = %v, want 2", m.Apps[1].Config.Replicas)
	}
	if m.Apps[1].Config.HibernateTimeoutMinutes == nil || *m.Apps[1].Config.HibernateTimeoutMinutes != 30 {
		t.Fatalf("app[1].Config.HibernateTimeoutMinutes = %v, want 30", m.Apps[1].Config.HibernateTimeoutMinutes)
	}
}

func TestParseManifest_BundleFileValid(t *testing.T) {
	src := `
fleet_id = "analytics"

[[bundle_file]]
from = "_shared/theme.py"
to = "helpers/theme.py"
consumers = ["sales", "operations"]

[[app]]
slug = "sales"
source = "./sales"

[[app]]
slug = "operations"
source = "./operations"
`
	m, probs := ParseManifest([]byte(src), "fleet.toml")
	if len(probs) != 0 {
		t.Fatalf("unexpected problems: %v", probs)
	}
	if len(m.BundleFiles) != 1 {
		t.Fatalf("len(BundleFiles) = %d, want 1", len(m.BundleFiles))
	}
	got := m.BundleFiles[0]
	if got.From != "_shared/theme.py" || got.To != "helpers/theme.py" ||
		len(got.Consumers) != 2 || got.Consumers[0] != "sales" || got.Consumers[1] != "operations" {
		t.Fatalf("BundleFiles[0] = %+v", got)
	}
}

func TestParseManifest_BundleFileProblems(t *testing.T) {
	const app = `
[[app]]
slug = "sales"
source = "./sales"
`
	cases := []struct {
		name, declarations, want string
	}{
		{"missing from", `[[bundle_file]]
to = "helpers/theme.py"
consumers = ["sales"]
`, "from is required"},
		{"missing to", `[[bundle_file]]
from = "_shared/theme.py"
consumers = ["sales"]
`, "to is required"},
		{"empty consumers", `[[bundle_file]]
from = "_shared/theme.py"
to = "helpers/theme.py"
consumers = []
`, "consumers must not be empty"},
		{"parent traversal", `[[bundle_file]]
from = "../theme.py"
to = "helpers/theme.py"
consumers = ["sales"]
`, `from "../theme.py"`},
		{"native separator", `[[bundle_file]]
from = "_shared/theme.py"
to = "helpers\\theme.py"
consumers = ["sales"]
`, "forward slashes"},
		{"reserved control file", `[[bundle_file]]
from = "_shared/shinyhub.toml"
to = "shinyhub.toml"
consumers = ["sales"]
`, "bundle control file"},
		{"unknown consumer", `[[bundle_file]]
from = "_shared/theme.py"
to = "helpers/theme.py"
consumers = ["missing"]
`, `unknown consumer "missing"`},
		{"duplicate consumer", `[[bundle_file]]
from = "_shared/theme.py"
to = "helpers/theme.py"
consumers = ["sales", "sales"]
`, `duplicate consumer "sales"`},
		{"destination prefix collision", `[[bundle_file]]
from = "_shared/a"
to = "helpers"
consumers = ["sales"]
[[bundle_file]]
from = "_shared/b"
to = "helpers/theme.py"
consumers = ["sales"]
`, "destination conflict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, probs := ParseManifest([]byte("fleet_id = \"analytics\"\n"+tc.declarations+app), "fleet.toml")
			if got := problemsString(probs); !strings.Contains(got, tc.want) {
				t.Fatalf("problems must contain %q\n--- got ---\n%s", tc.want, got)
			}
		})
	}
}

func TestParseManifest_Autoscale(t *testing.T) {
	src := `
fleet_id = "eu"

[[app]]
slug = "a"
source = "git+https://example.com/a.git"

  [app.config]
  autoscale = { enabled = true, min_replicas = 1, max_replicas = 8, target = 0.8 }
`
	m, probs := ParseManifest([]byte(src), "f.toml")
	if len(probs) != 0 {
		t.Fatalf("unexpected problems: %v", probs)
	}
	as := m.Apps[0].Config.Autoscale
	if as == nil {
		t.Fatal("Config.Autoscale = nil, want the declared block")
	}
	if as.Enabled == nil || !*as.Enabled || as.MinReplicas != 1 || as.MaxReplicas != 8 || as.Target != 0.8 {
		t.Fatalf("autoscale = %+v, want {enabled:true min:1 max:8 target:0.8}", as)
	}
}

func TestParseManifest_AutoscaleInvalid(t *testing.T) {
	// enabled omitted (incomplete block) and max < min: both flagged locally.
	src := `
fleet_id = "eu"

[[app]]
slug = "a"
source = "./a"

  [app.config]
  autoscale = { min_replicas = 5, max_replicas = 2 }
`
	_, probs := ParseManifest([]byte(src), "f.toml")
	joined := problemsString(probs)
	if !strings.Contains(joined, "autoscale.enabled is required") {
		t.Fatalf("missing enabled-required problem\n--- got ---\n%s", joined)
	}
}

func TestParseManifest_AutoscaleUnknownSubkey(t *testing.T) {
	src := `
fleet_id = "eu"

[[app]]
slug = "a"
source = "./a"

  [app.config]
  autoscale = { enabled = true, min_replicas = 1, max_replicas = 2, targett = 0.5 }
`
	_, probs := ParseManifest([]byte(src), "f.toml")
	if !strings.Contains(problemsString(probs), "unknown key") {
		t.Fatalf("expected unknown-key problem for a mistyped autoscale subkey, got %v", probs)
	}
}

func TestParseManifest_UnknownKeyRetainsPathWithoutSelfSuggestion(t *testing.T) {
	src := `
fleet_id = "eu"

[[project]]
slug = "analytics"
source = "./not-valid-for-a-project"
`
	_, probs := ParseManifest([]byte(src), "fleet.toml")
	joined := problemsString(probs)
	if !strings.Contains(joined, `unknown key "project.source"`) {
		t.Fatalf("unknown key must retain its dotted TOML path\n--- got ---\n%s", joined)
	}
	if strings.Contains(joined, `did you mean "source"`) {
		t.Fatalf("an exact leaf match must not suggest itself\n--- got ---\n%s", joined)
	}
}

func TestParseManifest_AggregatesAllProblems(t *testing.T) {
	src := `
[[app]]
slug = "dup"
source = "./a"

[[app]]
slug = "dup"
source = "./b"
visibility = "secret"

[[app]]
slug = "gamma"
source = "./c"
hibernate_timout_minutes = 5
`
	_, probs := ParseManifest([]byte(src), "shinyhub-fleet.toml")
	joined := problemsString(probs)
	for _, want := range []string{
		"fleet_id is required",
		`duplicate slug "dup"`,
		`invalid visibility "secret"`,
		`unknown key "app.hibernate_timout_minutes"`,
		`did you mean "hibernate_timeout_minutes"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("problems missing %q\n--- got ---\n%s", want, joined)
		}
	}
}

func TestParseManifest_TOMLSyntaxError(t *testing.T) {
	_, probs := ParseManifest([]byte("fleet_id = \nthis is not toml"), "f.toml")
	if len(probs) == 0 || !strings.Contains(problemsString(probs), "f.toml") {
		t.Fatalf("expected a parse problem mentioning the file, got %v", probs)
	}
}

func TestParseManifest_InvalidFleetID(t *testing.T) {
	_, probs := ParseManifest([]byte(`fleet_id = "Prod_EU!"`+"\n[[app]]\nslug=\"a\"\nsource=\"./a\"\n"), "f.toml")
	if !strings.Contains(problemsString(probs), "fleet_id") {
		t.Fatalf("expected fleet_id charset problem, got %v", probs)
	}
}

func TestParseManifest_ConfigBounds(t *testing.T) {
	src := `
fleet_id = "eu"

[[app]]
slug = "a"
source = "./a"

  [app.config]
  replicas = 0
  max_sessions_per_replica = 0
  hibernate_timeout_minutes = 0
`
	_, probs := ParseManifest([]byte(src), "f.toml")
	joined := problemsString(probs)
	for _, want := range []string{
		"replicas must be >= 1",
		"max_sessions_per_replica must be >= 1",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q\n--- got ---\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "hibernate_timeout_minutes") {
		t.Fatalf("hibernate_timeout_minutes=0 disables hibernation and must be accepted:\n%s", joined)
	}

	// -1 is the accepted reset sentinel for hibernate only.
	okSrc := `
fleet_id = "eu"

[[app]]
slug = "a"
source = "git+https://example.com/a.git"

  [app.config]
  hibernate_timeout_minutes = -1
  replicas = 1
`
	if _, p := ParseManifest([]byte(okSrc), "f.toml"); len(p) != 0 {
		t.Fatalf("hibernate -1 sentinel must be accepted, got: %v", p)
	}
}

func TestParseManifest_MissingSlugAndSource(t *testing.T) {
	src := `
fleet_id = "eu"

[[app]]
source = "./a"

[[app]]
slug = "b"
`
	_, probs := ParseManifest([]byte(src), "f.toml")
	joined := problemsString(probs)
	if !strings.Contains(joined, "missing slug") {
		t.Fatalf("expected a missing-slug problem, got:\n%s", joined)
	}
	if !strings.Contains(joined, "source is required") {
		t.Fatalf("expected a source-required problem, got:\n%s", joined)
	}
}

// problemsString is a test helper that joins problem messages newline-separated.
func problemsString(ps []Problem) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Error()
	}
	return strings.Join(out, "\n")
}

func TestValidFleetID(t *testing.T) {
	for _, ok := range []string{"prod-eu", "a", "fleet-123", "x" + strings_Repeat63()} {
		if !ValidFleetID(ok) {
			t.Errorf("ValidFleetID(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "Prod", "has_underscore", "spaces here", strings_Repeat65()} {
		if ValidFleetID(bad) {
			t.Errorf("ValidFleetID(%q) = true, want false", bad)
		}
	}
}

func strings_Repeat63() string { return repeatRune('a', 63) }
func strings_Repeat65() string { return repeatRune('a', 65) }
func repeatRune(r byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = r
	}
	return string(b)
}

func TestParseManifestProjects(t *testing.T) {
	m, probs := ParseManifest([]byte(`
fleet_id = "acme"

[[project]]
slug = "analytics"
name = "  Acme Analytics  "
description = "Revenue and forecasting."
icon = "📊"

[[app]]
slug = "revenue"
source = "./apps/revenue"

  [app.config]
  project = "analytics"
`), "fleet.toml")
	if len(probs) > 0 {
		t.Fatalf("unexpected problems: %v", probs)
	}
	if len(m.Projects) != 1 || m.Projects[0].Slug != "analytics" {
		t.Fatalf("Projects = %+v, want one analytics entry", m.Projects)
	}
	// Normalized in place, like Config.Name, so the diff compares the trimmed
	// value instead of reporting drift on every plan.
	if m.Projects[0].Name == nil || *m.Projects[0].Name != "Acme Analytics" {
		t.Errorf("Projects[0].Name = %v, want trimmed", m.Projects[0].Name)
	}
	if m.Apps[0].Config.Project == nil || *m.Apps[0].Config.Project != "analytics" {
		t.Errorf("Config.Project = %v, want analytics", m.Apps[0].Config.Project)
	}
}

func TestParseManifestProjectProblems(t *testing.T) {
	cases := []struct {
		name, toml, want string
	}{
		{"missing slug", `
fleet_id = "a"
[[project]]
name = "X"
`, "missing slug"},
		{"invalid slug", `
fleet_id = "a"
[[project]]
slug = "Not A Slug"
`, "invalid"},
		{"duplicate slug", `
fleet_id = "a"
[[project]]
slug = "p"
[[project]]
slug = "p"
[[app]]
slug = "x"
source = "./x"
  [app.config]
  project = "p"
`, "duplicate project"},
		{"unreferenced project", `
fleet_id = "a"
[[project]]
slug = "orphan"
`, "no app in this manifest"},
		{"name too long", `
fleet_id = "a"
[[project]]
slug = "p"
name = "` + strings.Repeat("x", 129) + `"
`, "name"},
		{"invalid icon", `
fleet_id = "a"
[[project]]
slug = "p"
icon = "not an emoji"
`, "icon"},
		{"invalid app project slug", `
fleet_id = "a"
[[app]]
slug = "x"
source = "./x"
  [app.config]
  project = "Not A Slug"
`, "project"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, probs := ParseManifest([]byte(tc.toml), "fleet.toml")
			if len(probs) == 0 {
				t.Fatalf("want a problem containing %q, got none", tc.want)
			}
			joined := ""
			for _, p := range probs {
				joined += p.Msg + "\n"
			}
			if !strings.Contains(joined, tc.want) {
				t.Errorf("problems %q must mention %q", joined, tc.want)
			}
		})
	}
}

func TestParseManifestProjectAllowsEmptyDeclarations(t *testing.T) {
	// A declared "" is an explicit clear on every one of these, not a validation
	// error: "" name falls back to the slug, "" icon clears the icon, and ""
	// project ungroups the app.
	m, probs := ParseManifest([]byte(`
fleet_id = "a"
[[project]]
slug = "p"
name = ""
icon = ""
[[app]]
slug = "x"
source = "./x"
  [app.config]
  project = "p"

[[app]]
slug = "y"
source = "./y"
  [app.config]
  project = ""
`), "fleet.toml")
	if len(probs) > 0 {
		t.Fatalf("unexpected problems: %v", probs)
	}
	if m.Apps[1].Config.Project == nil || *m.Apps[1].Config.Project != "" {
		t.Errorf(`app y Config.Project = %v, want a declared ""`, m.Apps[1].Config.Project)
	}
}

func TestKnownKeysSuggestsProjectKeys(t *testing.T) {
	// The did-you-mean list must cover the new keys, or a typo'd "porject"
	// reports "unknown key" with no suggestion.
	if s := suggest("porject", knownKeys); s != "project" {
		t.Errorf("suggest(porject) = %q, want project", s)
	}
	if s := suggest("icno", knownKeys); s != "icon" {
		t.Errorf("suggest(icno) = %q, want icon", s)
	}
}

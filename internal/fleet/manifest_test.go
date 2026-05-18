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
		`unknown key "hibernate_timout_minutes"`,
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
		"hibernate_timeout_minutes must be >= 1",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q\n--- got ---\n%s", want, joined)
		}
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

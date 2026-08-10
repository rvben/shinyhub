package fleet

import (
	"strings"
	"testing"
)

// A declared name/description parses into pointers and is normalized in place,
// so the diff compares the trimmed value against the server rather than
// reporting drift on every plan because of manifest padding.
func TestParseManifest_NameDescriptionNormalized(t *testing.T) {
	src := `
fleet_id = "eu"

[[app]]
slug = "a"
source = "./a"

  [app.config]
  name = "  Quarterly Revenue  "
  description = "  Regional roll-up  "
`
	m, probs := ParseManifest([]byte(src), "f.toml")
	if len(probs) != 0 {
		t.Fatalf("unexpected problems: %v", probs)
	}
	c := m.Apps[0].Config
	if c.Name == nil || *c.Name != "Quarterly Revenue" {
		t.Errorf("name = %v, want %q trimmed", c.Name, "Quarterly Revenue")
	}
	if c.Description == nil || *c.Description != "Regional roll-up" {
		t.Errorf("description = %v, want %q trimmed", c.Description, "Regional roll-up")
	}
}

// Absent keys stay nil so they are not reconciled at all.
func TestParseManifest_NameDescriptionAbsent(t *testing.T) {
	src := `
fleet_id = "eu"

[[app]]
slug = "a"
source = "./a"

  [app.config]
  replicas = 2
`
	m, probs := ParseManifest([]byte(src), "f.toml")
	if len(probs) != 0 {
		t.Fatalf("unexpected problems: %v", probs)
	}
	if c := m.Apps[0].Config; c.Name != nil || c.Description != nil {
		t.Errorf("absent keys decoded as %v/%v, want nil/nil", c.Name, c.Description)
	}
}

// An empty description is a legal declared value (it clears the field); an
// empty name is not, because the column is the app's primary label.
func TestParseManifest_NameDescriptionBounds(t *testing.T) {
	bad := `
fleet_id = "eu"

[[app]]
slug = "a"
source = "./a"

  [app.config]
  name = "   "
  description = "` + strings.Repeat("x", 281) + `"
`
	_, probs := ParseManifest([]byte(bad), "f.toml")
	joined := problemsString(probs)
	for _, want := range []string{"name must not be empty", "description must be 280 characters or fewer"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q\n--- got ---\n%s", want, joined)
		}
	}

	long := `
fleet_id = "eu"

[[app]]
slug = "a"
source = "./a"

  [app.config]
  name = "` + strings.Repeat("x", 129) + `"
`
	if _, p := ParseManifest([]byte(long), "f.toml"); !strings.Contains(problemsString(p), "name must be between 1 and 128") {
		t.Fatalf("over-long name not rejected, got: %v", p)
	}

	ok := `
fleet_id = "eu"

[[app]]
slug = "a"
source = "./a"

  [app.config]
  description = ""
`
	m, p := ParseManifest([]byte(ok), "f.toml")
	if len(p) != 0 {
		t.Fatalf(`declared empty description must be accepted, got: %v`, p)
	}
	if c := m.Apps[0].Config; c.Description == nil || *c.Description != "" {
		t.Errorf(`description = %v, want a declared ""`, c.Description)
	}
}

// Both keys are in knownKeys, so a typo gets a suggestion instead of a bare
// "unknown key".
func TestParseManifest_NameTypoSuggestion(t *testing.T) {
	src := `
fleet_id = "eu"

[[app]]
slug = "a"
source = "./a"

  [app.config]
  nmae = "Oops"
  descrption = "Oops"
`
	_, probs := ParseManifest([]byte(src), "f.toml")
	joined := problemsString(probs)
	if !strings.Contains(joined, `did you mean "name"`) {
		t.Errorf("no suggestion for a misspelled name\n--- got ---\n%s", joined)
	}
	if !strings.Contains(joined, `did you mean "description"`) {
		t.Errorf("no suggestion for a misspelled description\n--- got ---\n%s", joined)
	}
}

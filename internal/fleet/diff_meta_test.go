package fleet

import "testing"

// Display-metadata drift follows the same declared-only contract as the numeric
// keys: nil desired never drifts, a matching value never drifts, and an
// unobserved server (the create path) always asserts a declared value.
func TestConfigDrift_NameDescription(t *testing.T) {
	declared := AppEntry{
		Slug: "a", Source: "./a", Visibility: "private",
		Config: Config{Name: sp("Quarterly Revenue"), Description: sp("Regional roll-up")},
	}

	// Create path (nothing observed): both assert, server side renders "(unset)".
	d := configDrift(declared, ObservedApp{Slug: "a"})
	if len(d) != 2 {
		t.Fatalf("create drift = %+v, want 2 items", d)
	}
	if d[0].Key != "name" || d[0].Server != "(unset)" || d[0].Desired != `"Quarterly Revenue"` {
		t.Errorf("name drift = %+v, want name (unset) -> \"Quarterly Revenue\"", d[0])
	}
	if d[1].Key != "description" || d[1].Server != "(unset)" || d[1].Desired != `"Regional roll-up"` {
		t.Errorf("description drift = %+v", d[1])
	}

	// Update path: a rename on the server drifts back to the manifest, and the
	// server value is quoted so a padded or empty value stays visible.
	d = configDrift(declared, ObservedApp{Slug: "a", Name: sp("Renamed In UI"), Description: sp("Regional roll-up")})
	if len(d) != 1 || d[0].Key != "name" || d[0].Server != `"Renamed In UI"` {
		t.Fatalf("rename drift = %+v, want a single name item", d)
	}

	// Declared == observed -> no drift.
	match := ObservedApp{Slug: "a", Name: sp("Quarterly Revenue"), Description: sp("Regional roll-up")}
	if x := configDrift(declared, match); len(x) != 0 {
		t.Errorf("matching metadata must not drift, got %+v", x)
	}

	// Undeclared keys never drift, however different the server value is: that
	// is what keeps a dashboard rename working when the manifest stays silent.
	none := AppEntry{Slug: "a", Source: "./a", Config: Config{}}
	if x := configDrift(none, ObservedApp{Slug: "a", Name: sp("Anything"), Description: sp("At all")}); len(x) != 0 {
		t.Errorf("absent keys must not drift, got %+v", x)
	}
}

// An observed empty description is a real stored value, not "not observed": a
// declared "" must match it without drift, or every plan on an app with no
// description would show a phantom update. The inverse (declared "" against a
// non-empty server) must drift so the clear is actually applied.
func TestConfigDrift_DescriptionEmptyIsAValue(t *testing.T) {
	declared := AppEntry{Slug: "a", Source: "./a", Config: Config{Description: sp("")}}

	if x := configDrift(declared, ObservedApp{Slug: "a", Description: sp("")}); len(x) != 0 {
		t.Errorf(`declared "" vs stored "" must not drift, got %+v`, x)
	}

	x := configDrift(declared, ObservedApp{Slug: "a", Description: sp("Something")})
	if len(x) != 1 || x[0].Key != "description" || x[0].Desired != `""` {
		t.Fatalf(`declared "" vs stored value must drift to "", got %+v`, x)
	}
}

// DeclaredConfig is the create path's source of truth: a freshly created app
// starts at server defaults, so every declared key must appear.
func TestDeclaredConfig_IncludesNameAndDescription(t *testing.T) {
	got := DeclaredConfig(AppEntry{
		Slug: "a", Source: "./a", Visibility: "private",
		Config: Config{Name: sp("New App"), Description: sp("")},
	})
	keys := map[string]string{}
	for _, c := range got {
		keys[c.Key] = c.Desired
	}
	if keys["name"] != `"New App"` {
		t.Errorf("DeclaredConfig name = %q, want %q", keys["name"], `"New App"`)
	}
	if _, ok := keys["description"]; !ok {
		t.Error(`DeclaredConfig dropped a declared empty description; the create would leave the bundle's value in place`)
	}
	// Visibility is set by the deploy that creates the app, so it is excluded
	// here even though configDrift computes it.
	if _, ok := keys["visibility"]; ok {
		t.Error("DeclaredConfig must not include visibility")
	}
}

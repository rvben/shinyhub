package fleet

import "testing"

// sp(s string) *string is NOT declared here: it already exists at
// internal/fleet/diff_test.go:9, and both files are package fleet, so a second
// declaration fails to compile with "sp redeclared in this block". Use the
// existing one.

func projectManifest(entries ...ProjectEntry) *Manifest {
	return &Manifest{FleetID: "acme", Projects: entries}
}

func TestDiffProjectsCreate(t *testing.T) {
	d := DiffProjects(projectManifest(ProjectEntry{
		Slug: "analytics", Name: sp("Acme Analytics"), Icon: sp("📊"),
	}), nil)
	if len(d) != 1 {
		t.Fatalf("len = %d, want 1", len(d))
	}
	if d[0].Action != ActionCreate {
		t.Errorf("Action = %v, want create", d[0].Action)
	}
	// A create carries the declared metadata so the plan shows what it will set
	// and the apply can build a fully-named POST body.
	if len(d[0].Drift) != 2 {
		t.Errorf("Drift = %+v, want name and icon", d[0].Drift)
	}
}

func TestDiffProjectsUnchangedAndDrift(t *testing.T) {
	m := projectManifest(ProjectEntry{Slug: "p", Name: sp("P"), Description: sp("")})
	obs := []ObservedProject{{Slug: "p", Name: sp("P"), Description: sp(""), IconEmoji: sp("📊")}}

	d := DiffProjects(m, obs)
	if d[0].Action != ActionUnchanged {
		t.Errorf("Action = %v, want unchanged (icon is not declared, so it is not drift)", d[0].Action)
	}

	obs[0].Name = sp("Renamed in the dashboard")
	d = DiffProjects(m, obs)
	if d[0].Action != ActionUpdateConfig {
		t.Fatalf("Action = %v, want update(config)", d[0].Action)
	}
	if len(d[0].Drift) != 1 || d[0].Drift[0].Key != "name" {
		t.Errorf("Drift = %+v, want a single name item", d[0].Drift)
	}
}

func TestDiffProjectsNeverDeletes(t *testing.T) {
	// A project on the server that the manifest does not declare is never a
	// delete row: a fleet manifest may manage a subset of a server's apps, so
	// the project can be referenced by apps outside its scope.
	d := DiffProjects(projectManifest(), []ObservedProject{{Slug: "someone-elses"}})
	if len(d) != 0 {
		t.Errorf("DiffProjects = %+v, want no rows", d)
	}
}

func TestDiffProjectsSlugOnlyCreate(t *testing.T) {
	d := DiffProjects(projectManifest(ProjectEntry{Slug: "bare"}), nil)
	if d[0].Action != ActionCreate || len(d[0].Drift) != 0 {
		t.Errorf("got %+v, want a create with no drift items", d[0])
	}
}

func TestConfigDriftProject(t *testing.T) {
	app := AppEntry{Slug: "a", Config: Config{Project: sp("analytics")}}

	if got := configDrift(app, ObservedApp{ProjectSlug: sp("analytics")}); len(got) != 0 {
		t.Errorf("matching project must not drift, got %+v", got)
	}
	got := configDrift(app, ObservedApp{ProjectSlug: sp("other")})
	if len(got) != 1 || got[0].Key != "project" {
		t.Fatalf("got %+v, want a single project drift item", got)
	}
	if got[0].Desired != `"analytics"` || got[0].Server != `"other"` {
		t.Errorf("drift = %+v, want quoted values", got[0])
	}
	// An undeclared project is never drift, even against a grouped app.
	if d := configDrift(AppEntry{Slug: "a"}, ObservedApp{ProjectSlug: sp("x")}); len(d) != 0 {
		t.Errorf("undeclared project must not drift, got %+v", d)
	}
	// A declared "" against a grouped app IS drift: it ungroups the app.
	if d := configDrift(AppEntry{Slug: "a", Config: Config{Project: sp("")}},
		ObservedApp{ProjectSlug: sp("x")}); len(d) != 1 {
		t.Errorf(`a declared "" must drift against a grouped app, got %+v`, d)
	}
}

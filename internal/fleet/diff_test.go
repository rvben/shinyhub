package fleet

import (
	"sort"
	"testing"
)

func ptr(i int) *int      { return &i }
func sp(s string) *string { return &s }
func bp(b bool) *bool     { return &b }

func TestConfigDrift_Autoscale(t *testing.T) {
	on := AppEntry{
		Slug: "a", Source: "./a", Visibility: "private",
		Config: Config{Autoscale: &AutoscaleConfig{Enabled: bp(true), MinReplicas: 1, MaxReplicas: 8, Target: 0.8}},
	}

	// Create path (no observation): a declared block always asserts; the server
	// side renders "(unset)", mirroring the *int scalar drift convention.
	d := configDrift(on, ObservedApp{Slug: "a"})
	if len(d) != 1 || d[0].Key != "autoscale" || d[0].Server != "(unset)" || d[0].Desired != "on(1-8 @ 0.80)" {
		t.Fatalf("create drift = %+v, want [autoscale (unset) -> on(1-8 @ 0.80)]", d)
	}

	// Update path: declared differs from observed-off -> drift with server display.
	d = configDrift(on, ObservedApp{Slug: "a", Autoscale: &ObservedAutoscale{}})
	if len(d) != 1 || d[0].Server != "off" || d[0].Desired != "on(1-8 @ 0.80)" {
		t.Fatalf("update drift = %+v, want [autoscale off -> on(1-8 @ 0.80)]", d)
	}

	// Declared == observed -> no drift.
	match := ObservedApp{Slug: "a", Autoscale: &ObservedAutoscale{Enabled: true, MinReplicas: 1, MaxReplicas: 8, Target: 0.8}}
	if x := configDrift(on, match); len(x) != 0 {
		t.Errorf("matching autoscale must not drift, got %+v", x)
	}

	// No declared autoscale -> never drifts (server value wins).
	noAS := AppEntry{Slug: "a", Source: "./a", Config: Config{}}
	if x := configDrift(noAS, ObservedApp{Slug: "a", Autoscale: &ObservedAutoscale{Enabled: true, MinReplicas: 2, MaxReplicas: 5}}); len(x) != 0 {
		t.Errorf("absent declared autoscale must not drift, got %+v", x)
	}

	// An explicit autoscale-off with zero bounds must still assert on CREATE, so
	// it wins over a bundle shinyhub.toml that enables autoscale (fleet
	// precedence). Against an unset observation it produces a drift item.
	off := AppEntry{Slug: "a", Source: "./a", Config: Config{Autoscale: &AutoscaleConfig{Enabled: bp(false)}}}
	if x := configDrift(off, ObservedApp{Slug: "a"}); len(x) != 1 || x[0].Key != "autoscale" {
		t.Fatalf("explicit off on create must assert, got %+v", x)
	}
	// ...but explicit-off vs an already-off server is a no-op on update.
	if x := configDrift(off, ObservedApp{Slug: "a", Autoscale: &ObservedAutoscale{}}); len(x) != 0 {
		t.Errorf("off vs already-off must not drift, got %+v", x)
	}

	// Disabled on both sides but different remembered bounds still drifts, shown
	// with bounds rather than a confusing "off -> off".
	disM := AppEntry{Slug: "a", Source: "./a", Config: Config{
		Autoscale: &AutoscaleConfig{Enabled: bp(false), MinReplicas: 2, MaxReplicas: 8}}}
	dd := configDrift(disM, ObservedApp{Slug: "a", Autoscale: &ObservedAutoscale{MinReplicas: 1, MaxReplicas: 3}})
	if len(dd) != 1 || dd[0].Server == dd[0].Desired || dd[0].Server == "off" || dd[0].Desired == "off" {
		t.Errorf("disabled-bounds drift not shown clearly: %+v", dd)
	}
}

func mani(fleetID string, apps ...AppEntry) *Manifest {
	return &Manifest{FleetID: fleetID, Apps: apps}
}

func byslug(ds []AppDiff) map[string]AppDiff {
	m := map[string]AppDiff{}
	for _, d := range ds {
		m[d.Slug] = d
	}
	return m
}

func TestDiff_AllActions(t *testing.T) {
	m := mani("eu",
		AppEntry{Slug: "newapp", Source: "./n", Visibility: "private"},
		AppEntry{Slug: "owned-same", Source: "./a", Visibility: "private"},
		AppEntry{Slug: "owned-src", Source: "./b", Visibility: "private"},
		AppEntry{Slug: "owned-cfg", Source: "./c", Visibility: "private", Config: Config{Replicas: ptr(3)}},
		AppEntry{Slug: "owned-both", Source: "./d", Visibility: "private", Config: Config{Replicas: ptr(2)}},
		AppEntry{Slug: "adopt-null", Source: "./e", Visibility: "private"},
		AppEntry{Slug: "adopt-other", Source: "./f", Visibility: "private"},
	)
	local := map[string]string{
		"owned-same": "sha256:aaa",
		"owned-src":  "sha256:NEW",
		"owned-cfg":  "sha256:ccc",
		"owned-both": "sha256:NEW",
		"adopt-null": "sha256:zzz",
		"adopt-other": "sha256:zzz",
		"newapp":     "sha256:zzz",
	}
	observed := []ObservedApp{
		{Slug: "owned-same", ManagedBy: sp("fleet:eu"), ContentDigest: "sha256:aaa", Replicas: ptr(1)},
		{Slug: "owned-src", ManagedBy: sp("fleet:eu"), ContentDigest: "sha256:OLD", Replicas: ptr(1)},
		{Slug: "owned-cfg", ManagedBy: sp("fleet:eu"), ContentDigest: "sha256:ccc", Replicas: ptr(1)},
		{Slug: "owned-both", ManagedBy: sp("fleet:eu"), ContentDigest: "sha256:OLD", Replicas: ptr(1)},
		{Slug: "adopt-null", ManagedBy: nil, ContentDigest: "sha256:zzz"},
		{Slug: "adopt-other", ManagedBy: sp("fleet:us"), ContentDigest: "sha256:zzz"},
		{Slug: "stale-owned", ManagedBy: sp("fleet:eu"), ContentDigest: "sha256:q"},
		{Slug: "foreign", ManagedBy: sp("fleet:us"), ContentDigest: "sha256:q"},
		{Slug: "unmanaged-extra", ManagedBy: nil, ContentDigest: "sha256:q"},
	}

	got := byslug(Diff(m, local, observed))

	check := func(slug string, want Action) {
		if got[slug].Action != want {
			t.Errorf("%s: action = %q, want %q", slug, got[slug].Action, want)
		}
	}
	check("newapp", ActionCreate)
	check("owned-same", ActionUnchanged)
	check("owned-src", ActionUpdateSource)
	check("owned-cfg", ActionUpdateConfig)
	check("owned-both", ActionUpdateSourceConfig)
	check("adopt-null", ActionAdopt)
	check("adopt-other", ActionAdopt)
	check("stale-owned", ActionDelete)

	if _, ok := got["foreign"]; ok {
		t.Error("foreign (other fleet, not in manifest) must not appear")
	}
	if _, ok := got["unmanaged-extra"]; ok {
		t.Error("unmanaged app absent from manifest must not appear (never pruned)")
	}
	if !got["adopt-null"].AdoptRequired {
		t.Error("adopt-null.AdoptRequired = false, want true")
	}
	if !got["stale-owned"].PruneEligible {
		t.Error("stale-owned.PruneEligible = false, want true")
	}
	if d := got["owned-cfg"]; len(d.ConfigDrift) != 1 || d.ConfigDrift[0].Key != "replicas" ||
		d.ConfigDrift[0].Server != "1" || d.ConfigDrift[0].Desired != "3" {
		t.Errorf("owned-cfg.ConfigDrift = %+v, want [replicas 1->3]", d.ConfigDrift)
	}
}

// FLT-5: adopting an app owned by a DIFFERENT fleet is an ownership transfer,
// not a first-time adoption. The diff must surface the current foreign owner
// (AdoptFrom) so plan/apply can warn; a genuinely unmanaged app has no prior
// owner and leaves AdoptFrom empty.
func TestDiff_AdoptFromForeignFleet(t *testing.T) {
	m := mani("eu",
		AppEntry{Slug: "adopt-null", Source: "./e", Visibility: "private"},
		AppEntry{Slug: "adopt-other", Source: "./f", Visibility: "private"},
	)
	local := map[string]string{"adopt-null": "sha256:z", "adopt-other": "sha256:z"}
	observed := []ObservedApp{
		{Slug: "adopt-null", ManagedBy: nil, ContentDigest: "sha256:z"},
		{Slug: "adopt-other", ManagedBy: sp("fleet:us"), ContentDigest: "sha256:z"},
	}
	got := byslug(Diff(m, local, observed))

	if got["adopt-null"].AdoptFrom != "" {
		t.Errorf("unmanaged adopt-null.AdoptFrom = %q, want empty", got["adopt-null"].AdoptFrom)
	}
	if got["adopt-other"].AdoptFrom != "fleet:us" {
		t.Errorf("foreign-owned adopt-other.AdoptFrom = %q, want %q", got["adopt-other"].AdoptFrom, "fleet:us")
	}
}

func TestDiff_OrderIndependence(t *testing.T) {
	a := AppEntry{Slug: "a", Source: "./a", Visibility: "private"}
	b := AppEntry{Slug: "b", Source: "./b", Visibility: "private"}
	local := map[string]string{"a": "sha256:1", "b": "sha256:1"}
	obs := []ObservedApp{}
	d1 := Diff(mani("eu", a, b), local, obs)
	d2 := Diff(mani("eu", b, a), local, obs)
	sortBySlug := func(ds []AppDiff) {
		sort.Slice(ds, func(i, j int) bool { return ds[i].Slug < ds[j].Slug })
	}
	sortBySlug(d1)
	sortBySlug(d2)
	if len(d1) != 2 || d1[0].Action != ActionCreate || d1[1].Action != ActionCreate {
		t.Fatalf("d1 = %+v", d1)
	}
	for i := range d1 {
		if d1[i].Slug != d2[i].Slug || d1[i].Action != d2[i].Action {
			t.Fatalf("order-dependent result: %+v vs %+v", d1, d2)
		}
	}
}

func TestDiff_DigestEmptyServerCountsAsSourceChange(t *testing.T) {
	m := mani("eu", AppEntry{Slug: "x", Source: "./x", Visibility: "private"})
	local := map[string]string{"x": "sha256:abc"}
	obs := []ObservedApp{{Slug: "x", ManagedBy: sp("fleet:eu"), ContentDigest: ""}}
	got := byslug(Diff(m, local, obs))
	if got["x"].Action != ActionUpdateSource {
		t.Fatalf("legacy NULL digest must yield update(source), got %q", got["x"].Action)
	}
}

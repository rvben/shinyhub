package nativebroker

import (
	"strings"
	"testing"
)

func fixturePolicy() Policy {
	return Policy{ControlUID: 2000, ControlGID: 2000, Socket: "/run/shinyhub-broker/control.sock", RuntimeDir: "/run/shinyhub-broker", StateDir: "/var/lib/shinyhub-broker", Apps: []App{{ID: 1, Slug: "alpha", UID: 2001, GID: 2001, BundleRoot: "/srv/apps/alpha", DataRoot: "/srv/data/alpha", CacheRoot: "/srv/cache/alpha"}}}
}
func TestPolicyRejectsPrivilegeAndStorageOverlap(t *testing.T) {
	for _, change := range []func(*Policy){
		func(p *Policy) { p.Apps[0].UID = 0 }, func(p *Policy) { p.Apps[0].UID = p.ControlUID }, func(p *Policy) { p.Apps[0].GID = p.ControlGID },
		func(p *Policy) { p.Apps[0].DataRoot = "/srv/apps/alpha/data" }, func(p *Policy) { p.Apps[0].BundleRoot = "/" },
		func(p *Policy) { p.Apps[0].BundleRoot = "/srv/%n" }, func(p *Policy) { p.Socket = "/tmp/foreign.sock" },
		func(p *Policy) { p.Apps = append(p.Apps, p.Apps[0]) },
	} {
		p := fixturePolicy()
		change(&p)
		if p.Validate() == nil {
			t.Fatalf("unsafe policy accepted: %+v", p)
		}
	}
	if err := fixturePolicy().Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestLaunchRejectsForeignIdentityPathsAndDescriptors(t *testing.T) {
	for name, change := range map[string]func(*Launch){
		"different app": func(l *Launch) { l.AppID = 2 }, "different slug": func(l *Launch) { l.Slug = "beta" },
		"escaping cwd": func(l *Launch) { l.Dir = "/srv/apps/alpha/../beta" }, "other data": func(l *Launch) { l.DataDir = "/srv/data/beta" },
		"other cache": func(l *Launch) { l.CacheDir = "/srv/cache/beta" }, "shell injection kind": func(l *Launch) { l.Kind = "shell" },
		"relative executable": func(l *Launch) { l.Argv = []string{"python3"} }, "missing inherited locks": func(l *Launch) { l.LifetimeCount = 2 },
		"nul argument": func(l *Launch) { l.Argv = append(l.Argv, "bad\x00arg") }, "invalid env": func(l *Launch) { l.Env = []string{"BAD\x00=value"} },
	} {
		t.Run(name, func(t *testing.T) {
			l := Launch{AppID: 1, Slug: "alpha", Kind: "job", Dir: "/srv/apps/alpha/v1", Argv: []string{"/usr/bin/python3"}, Guarded: true}
			change(&l)
			if _, err := fixturePolicy().validateLaunch(&l, 2); err == nil {
				t.Fatal("unsafe launch accepted")
			}
		})
	}
}
func TestLaunchCarriesLocksWithoutCredentialProperties(t *testing.T) {
	l := Launch{AppID: 1, Slug: "alpha", Kind: "replica", Dir: "/srv/apps/alpha/v1", Argv: []string{"/usr/bin/python3"}, Env: []string{"SHINYHUB_IDENTITY_KEY=synthetic"}, Guarded: true, LifetimeCount: 3}
	if _, err := fixturePolicy().validateLaunch(&l, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := fixturePolicy().validateLaunch(&l, 4); err == nil || !strings.Contains(err.Error(), "descriptor") {
		t.Fatal("missing lock descriptor accepted")
	}
}

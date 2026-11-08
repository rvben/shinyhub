package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// versionSwitchFixture holds two apps, each with one deployment whose bundle
// directory the test controls.
type versionSwitchFixture struct {
	store             *db.Store
	alphaDep, betaDep int64
	alphaDir, betaDir string
	lookup            func(slug string, deploymentID int64) string
}

func newVersionSwitchFixture(t *testing.T) *versionSwitchFixture {
	t.Helper()
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	f := &versionSwitchFixture{store: store}
	for _, a := range []struct {
		slug string
		dir  *string
		dep  *int64
	}{{"alpha", &f.alphaDir, &f.alphaDep}, {"beta", &f.betaDir, &f.betaDep}} {
		if _, err := store.CreateApp(db.CreateAppParams{Slug: a.slug, Name: a.slug, ProjectSlug: "default", OwnerID: owner.ID, Access: "private"}); err != nil {
			t.Fatal(err)
		}
		app, err := store.GetAppBySlug(a.slug)
		if err != nil {
			t.Fatal(err)
		}
		*a.dir = t.TempDir()
		dep, err := store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "v1", BundleDir: *a.dir})
		if err != nil {
			t.Fatal(err)
		}
		*a.dep = dep.ID
	}
	f.lookup = versionSwitchPolicyLookup(store)
	return f
}

func writeVersionSwitch(t *testing.T, dir, policy string) {
	t.Helper()
	body := "[app]\nversion_switch = \"" + policy + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "shinyhub.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestVersionSwitchPolicyLookupReadsTheServedBundle(t *testing.T) {
	f := newVersionSwitchFixture(t)
	writeVersionSwitch(t, f.alphaDir, "when_hidden")
	if got := f.lookup("alpha", f.alphaDep); got != "when_hidden" {
		t.Fatalf("policy = %q, want when_hidden from the served bundle's manifest", got)
	}
}

// A served bundle is immutable, so its policy is read from disk once per
// deployment and every later request answers from memory.
func TestVersionSwitchPolicyLookupCachesPerDeployment(t *testing.T) {
	f := newVersionSwitchFixture(t)
	writeVersionSwitch(t, f.alphaDir, "when_hidden")
	if got := f.lookup("alpha", f.alphaDep); got != "when_hidden" {
		t.Fatalf("first lookup = %q, want when_hidden", got)
	}
	if err := os.Remove(filepath.Join(f.alphaDir, "shinyhub.toml")); err != nil {
		t.Fatal(err)
	}
	if got := f.lookup("alpha", f.alphaDep); got != "when_hidden" {
		t.Fatalf("second lookup = %q, want the cached when_hidden", got)
	}
}

// A deployment ID only counts for the app it belongs to: naming another
// app's deployment falls back to the safe default, whether or not that
// deployment's policy is already cached.
func TestVersionSwitchPolicyLookupRejectsAnotherAppsDeployment(t *testing.T) {
	f := newVersionSwitchFixture(t)
	writeVersionSwitch(t, f.alphaDir, "when_hidden")

	if got := f.lookup("beta", f.alphaDep); got != "manual" {
		t.Fatalf("uncached cross-app lookup = %q, want manual", got)
	}
	if got := f.lookup("alpha", f.alphaDep); got != "when_hidden" {
		t.Fatalf("owner lookup = %q, want when_hidden", got)
	}
	if got := f.lookup("beta", f.alphaDep); got != "manual" {
		t.Fatalf("cached cross-app lookup = %q, want manual", got)
	}
}

func TestVersionSwitchPolicyLookupDefaultsToManual(t *testing.T) {
	f := newVersionSwitchFixture(t)
	writeVersionSwitch(t, f.alphaDir, "when_hidden")
	for name, tc := range map[string]struct {
		slug string
		dep  int64
	}{
		"bundle without a manifest": {"beta", f.betaDep},
		"unknown app":               {"gamma", f.alphaDep},
		"unknown deployment":        {"alpha", f.alphaDep + f.betaDep + 1000},
	} {
		if got := f.lookup(tc.slug, tc.dep); got != "manual" {
			t.Errorf("%s: policy = %q, want manual", name, got)
		}
	}
}

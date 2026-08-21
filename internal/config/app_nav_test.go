package config_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

// The switcher is on unless an operator says otherwise, so an installation that
// upgrades into this version gets it without editing anything. Absent must
// therefore mean enabled - the zero value of a bool would mean the opposite,
// which is exactly why the field is a pointer.
func TestAppNav_DefaultsOn(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.AppNav != nil {
		t.Fatalf("an unset app_nav parsed as %v, want nil", *cfg.Server.AppNav)
	}
	if !cfg.Server.AppNavEnabled() {
		t.Error("app switcher is off by default; an upgrade would silently not get it")
	}
}

// An operator who writes app_nav: false is asking for application pages to be
// left alone. That has to be believed, and false is indistinguishable from
// absent unless the field records which it was.
func TestAppNav_YAMLFalseIsHonoured(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)

	cfg, err := config.Load(writeYAML(t, "server:\n  app_nav: false\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.AppNavEnabled() {
		t.Fatal("app_nav: false did not turn the switcher off")
	}
}

func TestAppNav_EnvOverridesYAML(t *testing.T) {
	// The env var is what a container deployment has; YAML is what the image
	// ships. The one supplied per deployment wins.
	for _, tc := range []struct {
		env  string
		want bool
	}{
		{env: "false", want: false},
		{env: "0", want: false},
		{env: "true", want: true},
		{env: "1", want: true},
	} {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)
			t.Setenv("SHINYHUB_SERVER_APP_NAV", tc.env)

			// YAML says the opposite of the environment in every case, so a
			// passing test cannot be one where the override was ignored.
			yaml := "server:\n  app_nav: true\n"
			if tc.want {
				yaml = "server:\n  app_nav: false\n"
			}
			cfg, err := config.Load(writeYAML(t, yaml))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.Server.AppNavEnabled(); got != tc.want {
				t.Fatalf("SHINYHUB_SERVER_APP_NAV=%q gave enabled=%v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

// A typo must not quietly pick a side. Reading "flase" as off would leave an
// operator who meant to keep the switcher wondering where it went; reading it
// as on would ignore an operator who meant to turn it off.
func TestAppNav_UnparseableEnvIsRejected(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)
	t.Setenv("SHINYHUB_SERVER_APP_NAV", "flase")

	if _, err := config.Load(""); err == nil {
		t.Fatal("an unparseable SHINYHUB_SERVER_APP_NAV was accepted")
	}
}

// Empty means unset, not false: an orchestrator that always exports the
// variable and leaves it blank must land on the default, not turn the feature
// off by accident.
func TestAppNav_EmptyEnvLeavesTheDefault(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)
	t.Setenv("SHINYHUB_SERVER_APP_NAV", "")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Server.AppNavEnabled() {
		t.Fatal("an empty SHINYHUB_SERVER_APP_NAV turned the switcher off")
	}
}

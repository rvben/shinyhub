package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvironmentConfigValidation(t *testing.T) {
	cases := []EnvironmentConfig{
		{}, {Label: strings.Repeat("a", 33)}, {Label: "bad\nlabel"}, {Label: "Test", Color: "red"},
		{Label: "Test", Message: strings.Repeat("a", 201)}, {Label: "Test", Message: "two\nlines"},
		{Label: "Test", ProductionURL: "javascript:bad"}, {Label: "Test", ProductionURL: "https://u:p@example.com"},
		{Label: "Test", ProductionURL: "https://example.com/sub"}, {Label: "Test", ProductionURL: "https://example.com/?x=1"},
		{Label: "Test", ProductionURL: "https://example.com/#secret"}, {Label: "Test", ProductionURL: "https://example.com/?"},
	}
	for _, e := range cases {
		t.Run(e.Label+e.ProductionURL+e.Color, func(t *testing.T) {
			if validateEnvironment(&e) == nil {
				t.Fatalf("accepted invalid config %#v", e)
			}
		})
	}
	e := EnvironmentConfig{Label: " Acceptance   West ", ProductionURL: "https://example.com/"}
	if err := validateEnvironment(&e); err != nil {
		t.Fatal(err)
	}
	if e.Label != "Acceptance West" || e.Color != "#f5b301" || e.Message != "" || e.ProductionURL != "https://example.com" {
		t.Fatalf("unexpected normalization %#v", e)
	}
	if (&EnvironmentConfig{Color: "#fff"}).TextColor() != "#000000" || (&EnvironmentConfig{Color: "#000"}).TextColor() != "#ffffff" {
		t.Fatal("contrast")
	}
}
func TestEnvironmentConfigLoadAndEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("auth:\n  secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nbranding:\n  environment:\n    label: Acceptance\n    color: '#abc'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHINYHUB_BRANDING_ENVIRONMENT_MESSAGE", "Test data")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Branding.IsActive() || cfg.Branding.Environment.Label != "Acceptance" || cfg.Branding.Environment.Message != "Test data" {
		t.Fatalf("%#v", cfg.Branding.Environment)
	}
}

func TestEmptyEnvironmentVariablesDoNotEnableIdentity(t *testing.T) {
	for _, key := range []string{"LABEL", "COLOR", "MESSAGE", "PRODUCTION_URL"} {
		t.Setenv("SHINYHUB_BRANDING_ENVIRONMENT_"+key, "")
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("auth:\n  secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Branding.Environment != nil {
		t.Fatal("empty variables enabled environment identity")
	}
}

package config_test

import (
	"reflect"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

func TestPublicAppAvailabilityConfiguration(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)
	path := writeYAML(t, "server:\n  public_app_availability: [sales, reports]\n")
	cfg, err := config.Load(path)
	if err != nil || !reflect.DeepEqual(cfg.Server.PublicAppAvailability, []string{"sales", "reports"}) {
		t.Fatalf("YAML allowlist: cfg=%+v err=%v", cfg, err)
	}
	t.Setenv("SHINYHUB_SERVER_PUBLIC_APP_AVAILABILITY", "demo, other")
	cfg, err = config.Load(path)
	if err != nil || !reflect.DeepEqual(cfg.Server.PublicAppAvailability, []string{"demo", "other"}) {
		t.Fatalf("environment override: cfg=%+v err=%v", cfg, err)
	}
	t.Setenv("SHINYHUB_SERVER_PUBLIC_APP_AVAILABILITY", "[]")
	cfg, err = config.Load(path)
	if err != nil || len(cfg.Server.PublicAppAvailability) != 0 {
		t.Fatalf("explicit empty list must disable exposure: cfg=%+v err=%v", cfg, err)
	}
	for _, invalid := range []string{"*", "sales,", "Sales", "../sales"} {
		t.Setenv("SHINYHUB_SERVER_PUBLIC_APP_AVAILABILITY", invalid)
		if _, err := config.Load(path); err == nil {
			t.Fatalf("accepted invalid allowlist %q", invalid)
		}
	}
}

func TestAvailabilityPolicyDefaultsAndOverrides(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)
	path := writeYAML(t, "server:\n  public_app_availability: [sales]\n")
	cfg, err := config.Load(path)
	if err != nil || cfg.Server.AppAvailabilityOptIn != "deny" || cfg.Server.PublicFleetAvailability {
		t.Fatalf("unsafe defaults: %+v %v", cfg, err)
	}
	t.Setenv("SHINYHUB_SERVER_APP_AVAILABILITY_OPT_IN", "allow")
	t.Setenv("SHINYHUB_SERVER_PUBLIC_FLEET_AVAILABILITY", "true")
	cfg, err = config.Load(path)
	if err != nil || cfg.Server.AppAvailabilityOptIn != "allow" || !cfg.Server.PublicFleetAvailability {
		t.Fatalf("env policy: %+v %v", cfg, err)
	}
	t.Setenv("SHINYHUB_SERVER_APP_AVAILABILITY_OPT_IN", "yes")
	if _, err := config.Load(path); err == nil {
		t.Fatal("accepted invalid opt-in policy")
	}
	t.Setenv("SHINYHUB_SERVER_APP_AVAILABILITY_OPT_IN", "deny")
	t.Setenv("SHINYHUB_SERVER_PUBLIC_FLEET_AVAILABILITY", "perhaps")
	if _, err := config.Load(path); err == nil {
		t.Fatal("accepted invalid discovery boolean")
	}
	t.Setenv("SHINYHUB_SERVER_PUBLIC_FLEET_AVAILABILITY", "false")
	cfg, err = config.Load(path)
	if err != nil || cfg.Server.AppAvailabilityOptIn != "deny" || cfg.Server.PublicFleetAvailability || len(cfg.Server.PublicAppAvailability) != 1 {
		t.Fatalf("policy override affected central allowlist: %+v %v", cfg, err)
	}
}

func TestAvailabilityDefaultConfiguration(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)
	path := writeYAML(t, "server:\n  app_availability_default: public\n")
	cfg, err := config.Load(path)
	if err != nil || cfg.Server.AppAvailabilityDefault != "public" || cfg.Server.AppAvailabilityOptIn != "deny" || cfg.Server.PublicFleetAvailability {
		t.Fatalf("independent settings: %+v %v", cfg, err)
	}
	t.Setenv("SHINYHUB_SERVER_APP_AVAILABILITY_DEFAULT", "off")
	cfg, err = config.Load(path)
	if err != nil || cfg.Server.AppAvailabilityDefault != "off" {
		t.Fatalf("override: %+v %v", cfg, err)
	}
	for _, v := range []string{"inherit", "yes", "PUBLIC"} {
		t.Setenv("SHINYHUB_SERVER_APP_AVAILABILITY_DEFAULT", v)
		if _, err := config.Load(path); err == nil {
			t.Fatalf("accepted default %q", v)
		}
	}
}

func TestAvailabilityDefaultOffYAML(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)
	for _, v := range []string{"off", `"off"`} {
		cfg, err := config.Load(writeYAML(t, "server:\n  app_availability_default: "+v+"\n"))
		if err != nil || cfg.Server.AppAvailabilityDefault != "off" {
			t.Fatalf("YAML %s: %+v %v", v, cfg, err)
		}
	}
}

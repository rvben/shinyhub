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

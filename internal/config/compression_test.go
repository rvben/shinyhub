package config_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

// Compression is on unless an operator says otherwise, so an installation that
// upgrades gets smaller responses without editing anything.
func TestCompression_DefaultsOn(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Compression != nil {
		t.Fatalf("an unset compression parsed as %v, want nil", *cfg.Server.Compression)
	}
	if !cfg.Server.CompressionEnabled() {
		t.Error("compression is off by default; an upgrade would silently not get it")
	}
}

func TestCompression_YAMLFalseIsHonoured(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)

	cfg, err := config.Load(writeYAML(t, "server:\n  compression: false\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.CompressionEnabled() {
		t.Fatal("compression: false did not turn compression off")
	}
}

func TestCompression_EnvOverridesYAML(t *testing.T) {
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
			t.Setenv("SHINYHUB_SERVER_COMPRESSION", tc.env)

			// YAML says the opposite of the environment in every case, so a
			// passing test cannot be one where the override was ignored.
			yaml := "server:\n  compression: true\n"
			if tc.want {
				yaml = "server:\n  compression: false\n"
			}
			cfg, err := config.Load(writeYAML(t, yaml))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.Server.CompressionEnabled(); got != tc.want {
				t.Fatalf("SHINYHUB_SERVER_COMPRESSION=%q gave enabled=%v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

func TestCompression_UnparseableEnvIsRejected(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)
	t.Setenv("SHINYHUB_SERVER_COMPRESSION", "flase")

	if _, err := config.Load(""); err == nil {
		t.Fatal("an unparseable SHINYHUB_SERVER_COMPRESSION was accepted")
	}
}

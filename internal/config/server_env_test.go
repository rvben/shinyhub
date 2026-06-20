package config_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

const testSecret = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

// TestServerHostPortEnv verifies the bind host and port can be set from the
// environment, which container deployments rely on instead of mounting YAML.
func TestServerHostPortEnv(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)
	t.Setenv("SHINYHUB_SERVER_HOST", "127.0.0.1")
	t.Setenv("SHINYHUB_SERVER_PORT", "9091")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("Server.Host = %q, want 127.0.0.1", cfg.Server.Host)
	}
	if cfg.Server.Port != 9091 {
		t.Errorf("Server.Port = %d, want 9091", cfg.Server.Port)
	}
}

// TestServerPortOutOfRangeRejected verifies a port outside the valid TCP range
// fails fast at config load instead of producing a cryptic net.Listen error.
func TestServerPortOutOfRangeRejected(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)
	t.Setenv("SHINYHUB_SERVER_PORT", "70000")

	if _, err := config.Load(""); err == nil {
		t.Fatal("expected an error for an out-of-range port, got nil")
	}
}

// TestTrustedProxiesEnvTrimsWhitespace verifies a comma+space separated CIDR
// list from the environment parses, rather than failing on a leading space.
// Uses RFC 5737 documentation ranges, never real LAN ranges.
func TestTrustedProxiesEnvTrimsWhitespace(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)
	t.Setenv("SHINYHUB_TRUSTED_PROXIES", "192.0.2.0/24, 198.51.100.0/24")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("whitespace after the comma should parse, got error: %v", err)
	}
	if len(cfg.TrustedProxyNets) != 2 {
		t.Fatalf("TrustedProxyNets has %d entries, want 2", len(cfg.TrustedProxyNets))
	}
}

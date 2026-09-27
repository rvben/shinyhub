package config_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

// WebSocket compression is on unless an operator says otherwise, so an upgrade
// gets smaller Shiny session traffic without editing anything.
func TestWebSocketCompression_DefaultsOn(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.WebSocketCompression != nil {
		t.Fatalf("an unset websocket_compression parsed as %v, want nil", *cfg.Server.WebSocketCompression)
	}
	if !cfg.Server.WebSocketCompressionEnabled() {
		t.Error("WebSocket compression is off by default; an upgrade would silently not get it")
	}
}

func TestWebSocketCompression_YAMLFalseIsHonoured(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)

	cfg, err := config.Load(writeYAML(t, "server:\n  websocket_compression: false\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.WebSocketCompressionEnabled() {
		t.Fatal("websocket_compression: false did not turn it off")
	}
}

func TestWebSocketCompression_EnvOverridesYAML(t *testing.T) {
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
			t.Setenv("SHINYHUB_SERVER_WEBSOCKET_COMPRESSION", tc.env)

			// YAML says the opposite of the environment in every case, so a
			// passing test cannot be one where the override was ignored.
			yaml := "server:\n  websocket_compression: true\n"
			if tc.want {
				yaml = "server:\n  websocket_compression: false\n"
			}
			cfg, err := config.Load(writeYAML(t, yaml))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.Server.WebSocketCompressionEnabled(); got != tc.want {
				t.Fatalf("SHINYHUB_SERVER_WEBSOCKET_COMPRESSION=%q gave enabled=%v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

func TestWebSocketCompression_UnparseableEnvIsRejected(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)
	t.Setenv("SHINYHUB_SERVER_WEBSOCKET_COMPRESSION", "flase")

	if _, err := config.Load(""); err == nil {
		t.Fatal("an unparseable SHINYHUB_SERVER_WEBSOCKET_COMPRESSION was accepted")
	}
}

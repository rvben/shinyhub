package config_test

import "testing"

func TestTracingASGIEvents(t *testing.T) {
	base := "auth:\n  secret: 0123456789abcdef0123456789abcdef\ntracing:\n  enabled: true\n  otlp_endpoint: http://collector:4318\n"
	cfg, err := loadFromString(t, base)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tracing.ASGIEvents {
		t.Fatal("ASGI events must default off")
	}
	cfg, err = loadFromString(t, base+"  asgi_events: true\n")
	if err != nil || !cfg.Tracing.ASGIEvents {
		t.Fatalf("YAML opt-in: %v", err)
	}
	t.Setenv("SHINYHUB_TRACING_ASGI_EVENTS", "false")
	cfg, err = loadFromString(t, base+"  asgi_events: true\n")
	if err != nil || cfg.Tracing.ASGIEvents {
		t.Fatalf("env override: %v", err)
	}
	t.Setenv("SHINYHUB_TRACING_ASGI_EVENTS", "garbage")
	if _, err := loadFromString(t, base); err == nil {
		t.Fatal("invalid env must fail")
	}
}

package config_test

import (
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

const tracingSecret = "auth:\n  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n"

// CFG-1: an explicit sample_ratio: 0 must disable sampling, not be silently
// promoted to the 0.1 default. The docs state "0 disables sampling".
func TestTracing_SampleRatioZeroHonored(t *testing.T) {
	path := writeYAML(t, tracingSecret+`
tracing:
  enabled: true
  otlp_endpoint: http://collector:4318
  sample_ratio: 0
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tracing.SampleRatio != 0 {
		t.Errorf("explicit sample_ratio: 0 should be honored, got %g", cfg.Tracing.SampleRatio)
	}
}

// CFG-1: an explicit ring_buffer_size: 0 must disable the buffer, not be
// promoted to 200.
func TestTracing_RingBufferSizeZeroHonored(t *testing.T) {
	path := writeYAML(t, tracingSecret+`
tracing:
  enabled: true
  otlp_endpoint: http://collector:4318
  ring_buffer_size: 0
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tracing.RingBufferSize != 0 {
		t.Errorf("explicit ring_buffer_size: 0 should be honored, got %d", cfg.Tracing.RingBufferSize)
	}
}

// CFG-1: an explicit slow_request_ms: 0 means "retain only error spans" per the
// field doc, not the 1000 default.
func TestTracing_SlowRequestMSZeroHonored(t *testing.T) {
	path := writeYAML(t, tracingSecret+`
tracing:
  enabled: true
  otlp_endpoint: http://collector:4318
  slow_request_ms: 0
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tracing.SlowRequestMS != 0 {
		t.Errorf("explicit slow_request_ms: 0 should be honored, got %d", cfg.Tracing.SlowRequestMS)
	}
}

// CFG-1: an explicit sample_ratio of 0 via env must also be honored.
func TestTracing_SampleRatioZeroViaEnvHonored(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_TRACING_ENABLED", "true")
	t.Setenv("SHINYHUB_TRACING_OTLP_ENDPOINT", "http://collector:4318")
	t.Setenv("SHINYHUB_TRACING_SAMPLE_RATIO", "0")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tracing.SampleRatio != 0 {
		t.Errorf("env SHINYHUB_TRACING_SAMPLE_RATIO=0 should be honored, got %g", cfg.Tracing.SampleRatio)
	}
}

// CFG-2: a non-numeric tracing env var is a misconfiguration that must fail
// loudly at startup, not be silently ignored leaving a surprise active value.
func TestTracing_InvalidSampleRatioEnvRejected(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_TRACING_ENABLED", "true")
	t.Setenv("SHINYHUB_TRACING_OTLP_ENDPOINT", "http://collector:4318")
	t.Setenv("SHINYHUB_TRACING_SAMPLE_RATIO", "hello")
	_, err := config.Load("")
	if err == nil {
		t.Fatal("expected error for non-numeric SHINYHUB_TRACING_SAMPLE_RATIO")
	}
	if !strings.Contains(err.Error(), "SHINYHUB_TRACING_SAMPLE_RATIO") {
		t.Errorf("error should name the offending env var: %v", err)
	}
}

func TestTracing_InvalidSlowRequestMSEnvRejected(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_TRACING_ENABLED", "true")
	t.Setenv("SHINYHUB_TRACING_OTLP_ENDPOINT", "http://collector:4318")
	t.Setenv("SHINYHUB_TRACING_SLOW_REQUEST_MS", "500ms")
	_, err := config.Load("")
	if err == nil {
		t.Fatal("expected error for non-numeric SHINYHUB_TRACING_SLOW_REQUEST_MS")
	}
	if !strings.Contains(err.Error(), "SHINYHUB_TRACING_SLOW_REQUEST_MS") {
		t.Errorf("error should name the offending env var: %v", err)
	}
}

// CFG-3: enabling tracing without an OTLP endpoint is a broken half-mode (apps
// get no OTEL_EXPORTER_OTLP_ENDPOINT) and must be rejected.
func TestTracing_EnabledWithoutEndpointRejected(t *testing.T) {
	path := writeYAML(t, tracingSecret+`
tracing:
  enabled: true
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for tracing enabled without otlp_endpoint")
	}
	if !strings.Contains(err.Error(), "otlp_endpoint") {
		t.Errorf("error should mention otlp_endpoint: %v", err)
	}
}

// CFG-4: a trace_link_template without the {trace_id} placeholder produces
// broken links and must be rejected.
func TestTracing_LinkTemplateWithoutPlaceholderRejected(t *testing.T) {
	path := writeYAML(t, tracingSecret+`
tracing:
  enabled: true
  otlp_endpoint: http://collector:4318
  trace_link_template: https://tempo.example/explore
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for trace_link_template missing {trace_id}")
	}
	if !strings.Contains(err.Error(), "trace_link_template") {
		t.Errorf("error should mention trace_link_template: %v", err)
	}
}

// CFG-5: common truthy spellings of the enabled env var must work.
func TestTracing_EnabledEnvAcceptsYesAndOn(t *testing.T) {
	for _, v := range []string{"yes", "on", "YES", "On", "1", "true"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
			t.Setenv("SHINYHUB_TRACING_ENABLED", v)
			t.Setenv("SHINYHUB_TRACING_OTLP_ENDPOINT", "http://collector:4318")
			cfg, err := config.Load("")
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !cfg.Tracing.Enabled {
				t.Errorf("SHINYHUB_TRACING_ENABLED=%q should enable tracing", v)
			}
		})
	}
}

// CFG-5: an unrecognized enabled value is a misconfiguration and must fail
// rather than silently disabling tracing.
func TestTracing_EnabledEnvRejectsGarbage(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_TRACING_ENABLED", "maybe")
	_, err := config.Load("")
	if err == nil {
		t.Fatal("expected error for SHINYHUB_TRACING_ENABLED=maybe")
	}
	if !strings.Contains(err.Error(), "SHINYHUB_TRACING_ENABLED") {
		t.Errorf("error should name the offending env var: %v", err)
	}
}

// Auto-instrumentation is opt-in and parses from YAML.
func TestTracing_AutoInstrumentAppsParsed(t *testing.T) {
	path := writeYAML(t, tracingSecret+`
tracing:
  enabled: true
  otlp_endpoint: http://collector:4318
  auto_instrument_apps: true
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Tracing.AutoInstrumentApps {
		t.Error("auto_instrument_apps: true should parse")
	}
}

// Default is off: today's behaviour is preserved exactly.
func TestTracing_AutoInstrumentAppsDefaultsOff(t *testing.T) {
	path := writeYAML(t, tracingSecret+`
tracing:
  enabled: true
  otlp_endpoint: http://collector:4318
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tracing.AutoInstrumentApps {
		t.Error("auto_instrument_apps should default to false")
	}
}

// Env override, symmetric with the rest of the tracing surface.
func TestTracing_AutoInstrumentAppsEnvOverride(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_TRACING_ENABLED", "true")
	t.Setenv("SHINYHUB_TRACING_OTLP_ENDPOINT", "http://collector:4318")
	t.Setenv("SHINYHUB_TRACING_AUTO_INSTRUMENT_APPS", "true")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Tracing.AutoInstrumentApps {
		t.Error("SHINYHUB_TRACING_AUTO_INSTRUMENT_APPS=true should enable auto-instrumentation")
	}
}

// A garbage env value must fail loudly, not silently disable.
func TestTracing_AutoInstrumentAppsEnvRejectsGarbage(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_TRACING_ENABLED", "true")
	t.Setenv("SHINYHUB_TRACING_OTLP_ENDPOINT", "http://collector:4318")
	t.Setenv("SHINYHUB_TRACING_AUTO_INSTRUMENT_APPS", "maybe")
	_, err := config.Load("")
	if err == nil {
		t.Fatal("expected error for SHINYHUB_TRACING_AUTO_INSTRUMENT_APPS=maybe")
	}
	if !strings.Contains(err.Error(), "SHINYHUB_TRACING_AUTO_INSTRUMENT_APPS") {
		t.Errorf("error should name the offending env var: %v", err)
	}
}

// auto_instrument_apps with tracing disabled is a broken half-mode: apps would
// be wrapped but export nowhere. Reject at startup like enabled-without-endpoint.
func TestTracing_AutoInstrumentRequiresEnabled(t *testing.T) {
	path := writeYAML(t, tracingSecret+`
tracing:
  auto_instrument_apps: true
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for auto_instrument_apps without tracing.enabled")
	}
	if !strings.Contains(err.Error(), "auto_instrument_apps") {
		t.Errorf("error should mention auto_instrument_apps: %v", err)
	}
}

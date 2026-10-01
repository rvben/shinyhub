package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

func TestProcessMetricsConfiguration(t *testing.T) {
	tracing := "tracing:\n  enabled: true\n  otlp_endpoint: http://collector:4318\n"
	for _, tc := range []struct {
		name, yaml, env string
		want            time.Duration
		errorText       string
	}{
		{name: "opt in", yaml: tracing + "metrics:\n  process_interval: 30s\n", want: 30 * time.Second},
		{name: "disabled by default"},
		{name: "endpoint alone does not enable", yaml: tracing},
		{name: "zero disables", yaml: "metrics:\n  process_interval: 0\n"},
		{name: "requires endpoint", yaml: "metrics:\n  process_interval: 30s\n", errorText: "requires tracing"},
		{name: "too frequent", yaml: tracing + "metrics:\n  process_interval: 100ms\n", errorText: "process_interval"},
		{name: "negative", yaml: tracing + "metrics:\n  process_interval: -1s\n", errorText: "process_interval"},
		{name: "too slow", yaml: tracing + "metrics:\n  process_interval: 11m\n", errorText: "process_interval"},
		{name: "malformed", yaml: tracing + "metrics:\n  process_interval: fast\n", errorText: "process_interval"},
		{name: "environment wins", yaml: tracing + "metrics:\n  process_interval: 100ms\n", env: "5s", want: 5 * time.Second},
		{name: "environment disables", yaml: "metrics:\n  process_interval: 30s\n", env: "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv("SHINYHUB_METRICS_PROCESS_INTERVAL", tc.env)
			}
			cfg, err := config.Load(writeYAML(t, metricsSecret+tc.yaml))
			if tc.errorText != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errorText) {
					t.Fatalf("error = %v, want %s", err, tc.errorText)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Metrics.ProcessInterval != tc.want {
				t.Fatalf("interval = %s, want %s", cfg.Metrics.ProcessInterval, tc.want)
			}
		})
	}
}

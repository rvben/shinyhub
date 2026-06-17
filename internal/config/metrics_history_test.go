package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

func TestMetricsHistory_Defaults(t *testing.T) {
	path := writeYAML(t, metricsSecret)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Metrics.HistoryWindow != 12*time.Hour {
		t.Errorf("history_window default = %v, want 12h", cfg.Metrics.HistoryWindow)
	}
	if cfg.Metrics.HistoryInterval != 15*time.Second {
		t.Errorf("history_interval default = %v, want 15s", cfg.Metrics.HistoryInterval)
	}
}

func TestMetricsHistory_FromYAML(t *testing.T) {
	path := writeYAML(t, metricsSecret+`
metrics:
  history_window: 6h
  history_interval: 30s
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Metrics.HistoryWindow != 6*time.Hour {
		t.Errorf("history_window = %v, want 6h", cfg.Metrics.HistoryWindow)
	}
	if cfg.Metrics.HistoryInterval != 30*time.Second {
		t.Errorf("history_interval = %v, want 30s", cfg.Metrics.HistoryInterval)
	}
}

func TestMetricsHistory_ZeroWindowDisables(t *testing.T) {
	path := writeYAML(t, metricsSecret+`
metrics:
  history_window: 0s
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Metrics.HistoryWindow != 0 {
		t.Errorf("history_window = %v, want 0 (disabled)", cfg.Metrics.HistoryWindow)
	}
}

func TestMetricsHistory_RejectsTinyInterval(t *testing.T) {
	path := writeYAML(t, metricsSecret+`
metrics:
  history_interval: 100ms
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for history_interval below the 1s floor")
	}
	if !strings.Contains(err.Error(), "history_interval") {
		t.Errorf("error should mention history_interval: %v", err)
	}
}

func TestMetricsHistory_RejectsHugeWindow(t *testing.T) {
	path := writeYAML(t, metricsSecret+`
metrics:
  history_window: 168h
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for history_window above the 48h ceiling")
	}
	if !strings.Contains(err.Error(), "history_window") {
		t.Errorf("error should mention history_window: %v", err)
	}
}

func TestMetricsHistory_RejectsTinyNonZeroWindow(t *testing.T) {
	path := writeYAML(t, metricsSecret+`
metrics:
  history_window: 30s
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for non-zero history_window below the 1m floor")
	}
	if !strings.Contains(err.Error(), "history_window") {
		t.Errorf("error should mention history_window: %v", err)
	}
}

func TestMetricsHistory_EnvOverride(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_METRICS_HISTORY_WINDOW", "6h")
	t.Setenv("SHINYHUB_METRICS_HISTORY_INTERVAL", "30s")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Metrics.HistoryWindow != 6*time.Hour {
		t.Errorf("history_window = %v, want 6h", cfg.Metrics.HistoryWindow)
	}
	if cfg.Metrics.HistoryInterval != 30*time.Second {
		t.Errorf("history_interval = %v, want 30s", cfg.Metrics.HistoryInterval)
	}
}

func TestMetricsHistory_EnvRejectsTinyInterval(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_METRICS_HISTORY_INTERVAL", "100ms")
	_, err := config.Load("")
	if err == nil {
		t.Fatal("expected error for SHINYHUB_METRICS_HISTORY_INTERVAL=100ms")
	}
	if !strings.Contains(err.Error(), "history_interval") {
		t.Errorf("error should mention history_interval: %v", err)
	}
}

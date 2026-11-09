package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/rungroup"
)

// observingRuntime is a tier runtime that accepts a workload observer, as the
// native runtime does. Every other Runtime method is unused here.
type observingRuntime struct {
	process.Runtime
	observer process.WorkloadObserver
}

func (r *observingRuntime) SetWorkloadObserver(o process.WorkloadObserver) { r.observer = o }

// opaqueRuntime is a tier runtime with no observer hook, like a container or
// remote runtime.
type opaqueRuntime struct{ process.Runtime }

func workloadMetricsConfig(endpoint string, interval time.Duration, tiers ...string) *config.Config {
	cfg := &config.Config{
		Server:  config.ServerConfig{Host: "127.0.0.1", Port: 8080},
		Tracing: config.TracingConfig{Enabled: true, OTLPEndpoint: endpoint, OTLPProtocol: "http/protobuf"},
		Metrics: config.MetricsConfig{ProcessInterval: interval},
	}
	for _, tier := range tiers {
		cfg.Runtime.Tiers = append(cfg.Runtime.Tiers, config.TierConfig{Name: tier})
	}
	return cfg
}

func TestStartWorkloadMetricsIsOffWithoutAnInterval(t *testing.T) {
	services := rungroup.New(slog.New(slog.DiscardHandler))
	t.Cleanup(func() { services.Stop() })
	stop, err := startWorkloadMetrics(workloadMetricsDeps{
		cfg:      workloadMetricsConfig("", 0),
		mgr:      process.NewManager(t.TempDir(), &observingRuntime{}),
		services: services,
		logger:   slog.New(slog.DiscardHandler),
	})
	if err != nil || stop != nil {
		t.Fatalf("startWorkloadMetrics with no interval = (stop set: %v, %v), want (no stop, nil)", stop != nil, err)
	}
}

// An interval without a usable exporter is a configuration error the server
// must refuse to start with, not silently skip.
func TestStartWorkloadMetricsRejectsAnUnusableExporter(t *testing.T) {
	services := rungroup.New(slog.New(slog.DiscardHandler))
	t.Cleanup(func() { services.Stop() })
	cfg := workloadMetricsConfig("", 5*time.Second)
	cfg.Tracing.Enabled = false
	stop, err := startWorkloadMetrics(workloadMetricsDeps{
		cfg:      cfg,
		mgr:      process.NewManager(t.TempDir(), &observingRuntime{}),
		services: services,
		logger:   slog.New(slog.DiscardHandler),
	})
	if err == nil || !strings.Contains(err.Error(), "workload metrics setup") {
		t.Fatalf("err = %v, want a workload metrics setup error", err)
	}
	if stop != nil {
		t.Fatal("a failed setup returned a stop func")
	}
}

func TestStartWorkloadMetricsObservesEveryCapableTierAndFlushesOnce(t *testing.T) {
	var exports atomic.Int32
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/metrics" {
			exports.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collector.Close)

	local, gpu := &observingRuntime{}, &observingRuntime{}
	mgr := process.NewManager(t.TempDir(), local)
	mgr.RegisterRuntime("gpu", gpu)
	mgr.RegisterRuntime("remote", &opaqueRuntime{})

	services := rungroup.New(slog.New(slog.DiscardHandler))
	t.Cleanup(func() { services.Stop() })
	// A long interval keeps the sampling loop from exporting on its own, so
	// any export seen here is the final flush.
	stop, err := startWorkloadMetrics(workloadMetricsDeps{
		cfg:      workloadMetricsConfig(collector.URL, 10*time.Minute, process.DefaultTier, "gpu", "remote"),
		mgr:      mgr,
		services: services,
		logger:   slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("startWorkloadMetrics: %v", err)
	}
	if stop == nil {
		t.Fatal("enabled workload metrics returned no stop func")
	}
	if local.observer == nil || gpu.observer == nil {
		t.Fatalf("observer installed: local=%v gpu=%v, want both", local.observer != nil, gpu.observer != nil)
	}

	// A workload that ran and finished is owed to the exporter.
	end := gpu.observer(process.StartParams{Slug: "app"}, process.RunHandle{PID: os.Getpid()}, "")
	if end == nil {
		t.Fatal("observer returned no completion func for a live PID")
	}
	end()

	stop()
	if got := exports.Load(); got != 1 {
		t.Fatalf("exports after stop = %d, want the final flush", got)
	}
	// Shutdown paths call stop more than once. A second flush would export
	// this late completion; the flush runs once, so it must not.
	if late := local.observer(process.StartParams{Slug: "late"}, process.RunHandle{PID: os.Getpid()}, ""); late != nil {
		late()
	}
	stop()
	if got := exports.Load(); got != 1 {
		t.Fatalf("exports after a second stop = %d, want the flush to run once", got)
	}
}

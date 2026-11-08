package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/rungroup"
	"github.com/rvben/shinyhub/internal/workloadmetrics"
)

// workloadFlushTimeout bounds the final export at shutdown.
const workloadFlushTimeout = 5 * time.Second

type workloadMetricsDeps struct {
	cfg      *config.Config
	mgr      *process.Manager
	services *rungroup.Group
	logger   *slog.Logger
}

// startWorkloadMetrics starts native workload OTLP metrics when
// metrics.process_interval is set. It hooks the collector into every tier
// runtime that can observe workloads and runs the sampler as a supervised
// service. The returned stop is idempotent and safe to call from several
// shutdown paths; it is nil when the feature is off.
func startWorkloadMetrics(d workloadMetricsDeps) (stop func(), err error) {
	cfg := d.cfg
	if cfg.Metrics.ProcessInterval <= 0 {
		return nil, nil
	}
	// The ownership-lease ID defaults to hostname-PID and changes on restart.
	// Counts need the stable server slot, while launch CPU streams stay unique.
	listener := net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port))
	collector, err := workloadmetrics.New(cfg.Tracing, cfg.Metrics.ProcessInterval, listener)
	if err != nil {
		return nil, fmt.Errorf("workload metrics setup: %w", err)
	}
	for _, tier := range cfg.Runtime.Tiers {
		if native, ok := d.mgr.RuntimeForTier(tier.Name).(interface {
			SetWorkloadObserver(process.WorkloadObserver)
		}); ok {
			native.SetWorkloadObserver(collector.Observe)
		}
	}
	service := d.services.Go("workload metrics", serviceMemoryStopBudget, collector.Run)
	var once sync.Once
	stop = func() {
		once.Do(func() {
			// The final export runs only once sampling has stopped, so no
			// observation lands after the flush.
			if !service.Stop() {
				return
			}
			flushCtx, cancel := context.WithTimeout(context.Background(), workloadFlushTimeout)
			defer cancel()
			if err := collector.Shutdown(flushCtx); err != nil {
				d.logger.Warn("workload metrics shutdown failed")
			}
		})
	}
	d.logger.Info("native workload metrics enabled", "interval", cfg.Metrics.ProcessInterval)
	return stop, nil
}

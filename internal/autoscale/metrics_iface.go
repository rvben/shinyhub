package autoscale

// AutoscaleMetrics records autoscale scale events for Prometheus.
// *metrics.Registry satisfies it. Kept as an interface so the autoscale
// package does not import Prometheus.
type AutoscaleMetrics interface {
	RecordAutoscaleScale(direction string) // direction: "up" or "down"
}

// SetMetrics wires a recorder for autoscale Prometheus metrics. Called once
// at startup before Run; nil-safe so tests that do not need metrics can skip
// it. Mirrors lifecycle.Watcher.SetMetrics and fargate.Runtime.SetMetrics.
func (c *Controller) SetMetrics(m AutoscaleMetrics) {
	if m != nil {
		c.metrics = m
	}
}

// Package workloadmetrics exports native workload resource observations in
// batched OTLP requests. It owns its sampling state; API and history sampling
// have independent CPU baselines.
package workloadmetrics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/process"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

const maxCompleted = 4096

type exporter interface {
	Export(context.Context, []*metricspb.ResourceMetrics) error
	Close() error
}

type workload struct {
	slug             string
	pid              int32
	created          int64
	start            uint64
	attrs            []*commonpb.KeyValue
	job              bool
	cgroup           string
	cpuBaseline, cpu float64
	cpuTime          uint64
	rss              int64
	rssTime          uint64
	members          map[int32]reading
}

// Collector keeps current observations plus a bounded completion queue. Only
// explicitly configured native runtimes call Observe; remote PIDs are never read.
type Collector struct {
	mu        sync.Mutex
	active    map[string]*workload
	apps      map[string]struct{}
	completed []*metricspb.ResourceMetrics
	base      map[string]string
	interval  time.Duration
	sampler   sampler
	exporter  exporter
	now       func() time.Time
}

// New uses the listener address to identify a stable server slot on this host.
// Ownership-lease IDs may contain a PID and must not be used for this identity.
func New(cfg config.TracingConfig, interval time.Duration, listener string) (*Collector, error) {
	if !cfg.Enabled || cfg.OTLPEndpoint == "" || interval < time.Second || interval > 10*time.Minute {
		return nil, fmt.Errorf("workload metrics requires tracing, an OTLP endpoint and an interval between 1s and 10m")
	}
	exp, err := newExporter(cfg)
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	instanceID := host
	if listener != "" {
		instanceID += "/" + listener
	}
	base := make(map[string]string, len(cfg.ResourceAttributes)+3)
	for k, v := range cfg.ResourceAttributes {
		base[k] = v
	}
	base["host.name"] = host
	base["service.name"] = "shinyhub"
	// Replica-count resources survive exporter restarts, so summing current
	// host counts cannot include both an old and a new exporter incarnation.
	// Individual workload streams below still receive unique launch identities.
	base["service.instance.id"] = instanceID
	return &Collector{
		active: make(map[string]*workload), apps: make(map[string]struct{}),
		base: base, interval: interval, sampler: newSampler(), exporter: exp, now: time.Now,
	}, nil
}

// Observe takes only the explicitly designated resource attributes out of the
// launch environment, retaining no command line, unrelated env vars or secrets.
// Authoritative workload identity overrides app-provided identity keys.
func (c *Collector) Observe(p process.StartParams, handle process.RunHandle, cgroup string) func() {
	if handle.PID <= 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	attrs := make(map[string]string)
	for k, v := range c.base {
		attrs[k] = v
	}
	attrs["service.name"] = p.Slug
	if p.AppVersion != "" {
		attrs["service.version"] = p.AppVersion
	}
	for _, env := range [][]string{p.Env, p.SecretEnv} {
		for _, kv := range env {
			if value, ok := strings.CutPrefix(kv, "OTEL_RESOURCE_ATTRIBUTES="); ok {
				for _, pair := range strings.Split(value, ",") {
					key, value, ok := strings.Cut(pair, "=")
					if !ok || strings.TrimSpace(key) == "" {
						continue
					}
					decoded, err := url.PathUnescape(strings.TrimSpace(value))
					if err == nil {
						attrs[strings.TrimSpace(key)] = decoded
					}
				}
			}
			if name, ok := strings.CutPrefix(kv, "OTEL_SERVICE_NAME="); ok && name != "" {
				attrs["service.name"] = name
			}
		}
	}
	attrs["shinyhub.app"], attrs["shinyhub.app.slug"] = p.Slug, p.Slug
	delete(attrs, "shinyhub.replica")
	delete(attrs, "shinyhub.schedule")
	delete(attrs, "shinyhub.schedule.run_id")
	delete(attrs, "shinyhub.schedule.name")
	if p.JobRunID != 0 {
		attrs["shinyhub.schedule"], attrs["shinyhub.schedule.name"] = p.JobSchedule, p.JobSchedule
		attrs["shinyhub.schedule.run_id"] = strconv.FormatInt(p.JobRunID, 10)
	} else {
		attrs["shinyhub.replica"] = strconv.Itoa(p.Index)
	}
	if p.DeploymentID > 0 {
		attrs["shinyhub.deployment.id"] = strconv.FormatInt(p.DeploymentID, 10)
	}
	attrs["host.name"] = c.base["host.name"]
	id := uuid.NewString()
	attrs["service.instance.id"] = id
	w := &workload{slug: p.Slug, pid: int32(handle.PID), start: uint64(c.now().UnixNano()),
		attrs: keyValues(attrs), job: p.JobRunID != 0, members: make(map[int32]reading)}
	if cgroup != "" {
		if cpu, err := c.sampler.cgroup(cgroup); err == nil {
			w.cgroup, w.cpuBaseline = cgroup, cpu
		}
	}
	// A launch-time observation survives even if a short job completes before
	// the next export tick. It does not claim to capture the interpreter's peak.
	c.sampler.sample(w, []int32{w.pid}, w.start)
	if w.created == 0 {
		// An identity we could not establish at launch must never be filled
		// in later from a potentially reused PID. Count the supervised replica,
		// but omit its resource measurements rather than attributing another PID.
		w.created = -1
	}
	c.active[id] = w
	if !w.job {
		c.apps[p.Slug] = struct{}{}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			// Cgroup CPU survives the leader's exit and is read before teardown.
			// Otherwise keep the last observation, with its original timestamp.
			if w.cgroup != "" {
				c.sampler.sample(w, nil, uint64(c.now().UnixNano()))
			}
			delete(c.active, id)
			if rm := workloadMetrics(w, 0); rm != nil {
				if len(c.completed) == maxCompleted {
					copy(c.completed, c.completed[1:])
					c.completed = c.completed[:maxCompleted-1]
					slog.Warn("workload metrics completion queue full; oldest observation dropped")
				}
				c.completed = append(c.completed, rm)
			}
		})
	}
}

func keyValues(attrs map[string]string) []*commonpb.KeyValue {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*commonpb.KeyValue, 0, len(keys))
	for _, k := range keys {
		out = append(out, &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: attrs[k]}}})
	}
	return out
}

func resourceMetrics(attrs []*commonpb.KeyValue, metrics ...*metricspb.Metric) *metricspb.ResourceMetrics {
	return &metricspb.ResourceMetrics{Resource: &resourcepb.Resource{Attributes: attrs}, ScopeMetrics: []*metricspb.ScopeMetrics{{
		Scope: &commonpb.InstrumentationScope{Name: "github.com/rvben/shinyhub/workloadmetrics"}, Metrics: metrics,
	}}}
}

func workloadMetrics(w *workload, at uint64) *metricspb.ResourceMetrics {
	var metrics []*metricspb.Metric
	if w.rssTime != 0 && (at == 0 || w.rssTime == at) {
		metrics = append(metrics, &metricspb.Metric{Name: "shinyhub.process.memory.usage", Unit: "By",
			Description: "Summed RSS of observed native process-group members; shared pages may be counted more than once.",
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{{
				TimeUnixNano: w.rssTime, Value: &metricspb.NumberDataPoint_AsInt{AsInt: w.rss},
			}}}}})
	}
	if w.cpuTime != 0 && (at == 0 || w.cpuTime == at) {
		accounting := "sampled"
		if w.cgroup != "" {
			accounting = "cgroup"
		}
		metrics = append(metrics, &metricspb.Metric{Name: "shinyhub.process.cpu.time", Unit: "s",
			Description: "Cumulative observed user and system CPU seconds since registration; sampled accounting can miss short-lived children.",
			Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{IsMonotonic: true, AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: w.cpuTime, StartTimeUnixNano: w.start,
					Attributes: keyValues(map[string]string{"shinyhub.process.cpu.accounting": accounting}), Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: w.cpu},
				}},
			}}})
	}
	if len(metrics) == 0 {
		return nil
	}
	return resourceMetrics(w.attrs, metrics...)
}

type batch struct {
	resources []*metricspb.ResourceMetrics
	completed []*metricspb.ResourceMetrics
	zeros     []string
}

func (c *Collector) collect() batch {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := uint64(c.now().UnixNano())
	var groups map[int32][]int32
	var err error
	if len(c.active) > 0 {
		groups, err = c.sampler.groups()
	}
	if err != nil {
		slog.Warn("workload metrics process scan failed")
	}
	b := batch{completed: append([]*metricspb.ResourceMetrics(nil), c.completed...)}
	b.resources = append(b.resources, b.completed...)
	counts := make(map[string]int64)
	for _, w := range c.active {
		if !w.job {
			counts[w.slug]++
		}
		// Failed scans produce no replacement RSS/CPU sample. Keep the last
		// real sample for completion, but never emit stale gauges every tick.
		if err != nil {
			continue
		}
		members := groups[w.pid]
		if len(members) == 0 {
			members = []int32{w.pid}
		}
		c.sampler.sample(w, members, now)
		if w.rssTime != now && w.cpuTime != now {
			continue
		}
		if rm := workloadMetrics(w, now); rm != nil {
			b.resources = append(b.resources, rm)
		}
	}
	for slug := range c.apps {
		attrs := make(map[string]string, len(c.base)+2)
		for k, v := range c.base {
			attrs[k] = v
		}
		attrs["shinyhub.app"], attrs["shinyhub.app.slug"] = slug, slug
		b.resources = append(b.resources, resourceMetrics(keyValues(attrs), &metricspb.Metric{
			Name: "shinyhub.replicas", Unit: "{replica}", Description: "Native replica processes supervised by this instance, including starting, draining and frozen replicas; excludes schedules.",
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: now, Value: &metricspb.NumberDataPoint_AsInt{AsInt: counts[slug]}}}}},
		}))
		if counts[slug] == 0 {
			b.zeros = append(b.zeros, slug)
		}
	}
	return b
}

func (c *Collector) export(ctx context.Context, b batch) error {
	// A queued batch may have been collected before an earlier export acked
	// the same completion. Remove those completions before sending it.
	c.mu.Lock()
	pending := make(map[*metricspb.ResourceMetrics]struct{}, len(c.completed))
	for _, rm := range c.completed {
		pending[rm] = struct{}{}
	}
	wasCompleted := make(map[*metricspb.ResourceMetrics]struct{}, len(b.completed))
	for _, rm := range b.completed {
		wasCompleted[rm] = struct{}{}
	}
	resources := make([]*metricspb.ResourceMetrics, 0, len(b.resources))
	for _, rm := range b.resources {
		_, completed := wasCompleted[rm]
		_, stillPending := pending[rm]
		if !completed || stillPending {
			resources = append(resources, rm)
		}
	}
	c.mu.Unlock()
	if len(resources) == 0 {
		return nil
	}
	err := c.exporter.Export(ctx, resources)
	var failure *exportError
	if err != nil && (!errors.As(err, &failure) || failure.retryable) {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	acked := make(map[*metricspb.ResourceMetrics]struct{}, len(b.completed))
	for _, rm := range b.completed {
		acked[rm] = struct{}{}
	}
	kept := c.completed[:0]
	for _, rm := range c.completed {
		if _, ok := acked[rm]; !ok {
			kept = append(kept, rm)
		}
	}
	clear(c.completed[len(kept):])
	c.completed = kept
	for _, slug := range b.zeros {
		active := false
		for _, w := range c.active {
			if !w.job && w.slug == slug {
				active = true
				break
			}
		}
		if !active {
			delete(c.apps, slug)
		}
	}
	return err
}

// Run keeps sampling independent of slow network requests. A single bounded
// export slot coalesces live snapshots; completed workloads remain queued until
// a successful export, subject to maxCompleted. Failed requests retry next tick.
func (c *Collector) Run(ctx context.Context) {
	requests := make(chan batch, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var nextAttempt time.Time
		backoff := time.Second
		for {
			select {
			case <-ctx.Done():
				return
			case b := <-requests:
				if time.Now().Before(nextAttempt) {
					continue
				}
				exportCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := c.export(exportCtx, b)
				if err != nil && ctx.Err() == nil {
					var failure *exportError
					if errors.As(err, &failure) && !failure.retryable {
						slog.Warn("workload metrics export rejected; observations dropped", "reason", failure.Error())
						backoff = time.Second
					} else {
						delay := backoff
						if failure != nil && failure.delay > delay {
							delay = failure.delay
						}
						nextAttempt = time.Now().Add(delay)
						backoff = min(backoff*2, time.Minute)
						slog.Warn("workload metrics export failed; retry scheduled", "retry_after", delay)
					}
				} else if err == nil {
					backoff = time.Second
				}
				cancel()
			}
		}
	}()
	defer wg.Wait()
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b := c.collect()
			select {
			case <-requests:
			default:
			}
			requests <- b
		}
	}
}

// Shutdown is called after Run is cancelled and joined and after job/process
// draining, so final observations can be flushed with a separate bounded context.
func (c *Collector) Shutdown(ctx context.Context) error {
	err := c.export(ctx, c.collect())
	closeErr := c.exporter.Close()
	if err != nil {
		return err
	}
	return closeErr
}

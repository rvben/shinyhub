package workloadmetrics

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

type recordingExporter struct {
	mu      sync.Mutex
	fail    bool
	batches [][]*metricspb.ResourceMetrics
}

func (e *recordingExporter) Export(_ context.Context, data []*metricspb.ResourceMetrics) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fail {
		return errors.New("offline")
	}
	e.batches = append(e.batches, data)
	return nil
}
func (*recordingExporter) Close() error { return nil }

func testCollector() (*Collector, map[int32]reading) {
	readings := map[int32]reading{10: {created: 100, rss: 10, cpu: 1}}
	c := &Collector{active: make(map[string]*workload), apps: make(map[string]struct{}), base: map[string]string{"deployment.environment.name": "test"},
		exporter: &recordingExporter{}, interval: time.Second, now: func() time.Time { return time.Unix(1000, 0) }}
	c.sampler = sampler{process: func(pid int32) (reading, error) {
		r, ok := readings[pid]
		if !ok {
			return reading{}, errors.New("gone")
		}
		return r, nil
	}, groups: func() (map[int32][]int32, error) {
		var members []int32
		for pid := range readings {
			members = append(members, pid)
		}
		return map[int32][]int32{10: members}, nil
	}, cgroup: func(string) (float64, error) { return 0, errors.New("unsupported") }}
	return c, readings
}

func metricsNamed(b batch, name string) []*metricspb.Metric {
	var result []*metricspb.Metric
	for _, rm := range b.resources {
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == name {
					result = append(result, m)
				}
			}
		}
	}
	return result
}

func TestCPUCounterRetainsExitedChildrenAndHandlesPIDReuse(t *testing.T) {
	c, readings := testCollector()
	end := c.Observe(process.StartParams{Slug: "app", Index: 2}, process.RunHandle{PID: 10}, "")
	defer end()
	readings[11] = reading{created: 1000001, cpu: 3, rss: 20}
	c.now = func() time.Time { return time.Unix(1001, 0) }
	b := c.collect()
	if got := metricsNamed(b, "shinyhub.process.cpu.time")[0].GetSum().DataPoints[0].GetAsDouble(); got != 3 {
		t.Fatalf("initial CPU = %v", got)
	}
	if got := metricsNamed(b, "shinyhub.process.memory.usage")[0].GetGauge().DataPoints[0].GetAsInt(); got != 30 {
		t.Fatalf("group RSS = %v", got)
	}
	delete(readings, 11)
	readings[10] = reading{created: 100, cpu: 2, rss: 10}
	b = c.collect()
	if got := metricsNamed(b, "shinyhub.process.cpu.time")[0].GetSum().DataPoints[0].GetAsDouble(); got != 4 {
		t.Fatalf("CPU after child exit = %v", got)
	}
	readings[11] = reading{created: 1001001, cpu: 1, rss: 15}
	b = c.collect()
	if got := metricsNamed(b, "shinyhub.process.cpu.time")[0].GetSum().DataPoints[0].GetAsDouble(); got != 5 {
		t.Fatalf("CPU after PID reuse = %v", got)
	}
	readings[10] = reading{created: 300, cpu: 500, rss: 999}
	c.now = func() time.Time { return time.Unix(1002, 0) }
	b = c.collect()
	if len(metricsNamed(b, "shinyhub.process.memory.usage")) != 0 || len(metricsNamed(b, "shinyhub.process.cpu.time")) != 0 {
		t.Fatal("sampled an unrelated reused root PID")
	}
}

func TestCgroupCPUIncludesUnobservedChildrenAndFinalConsumption(t *testing.T) {
	c, readings := testCollector()
	cpu := 20.0 // reused cgroup's pre-registration consumption is excluded
	c.sampler.cgroup = func(string) (float64, error) { return cpu, nil }
	end := c.Observe(process.StartParams{Slug: "app", JobSchedule: "refresh", JobRunID: 7}, process.RunHandle{PID: 10}, "/dedicated")
	cpu = 25
	c.now = func() time.Time { return time.Unix(1001, 0) }
	b := c.collect()
	point := metricsNamed(b, "shinyhub.process.cpu.time")[0].GetSum().DataPoints[0]
	if point.GetAsDouble() != 5 || point.Attributes[0].Value.GetStringValue() != "cgroup" {
		t.Fatalf("CPU point = %v", point)
	}
	delete(readings, 10)
	cpu = 27
	end()
	b = c.collect()
	if got := metricsNamed(b, "shinyhub.process.cpu.time")[0].GetSum().DataPoints[0].GetAsDouble(); got != 7 {
		t.Fatalf("final CPU = %v", got)
	}
	if len(metricsNamed(b, "shinyhub.replicas")) != 0 {
		t.Fatal("schedule counted as a replica")
	}
}

func TestShortRunRetainedAcrossOutageThenRemoved(t *testing.T) {
	c, readings := testCollector()
	end := c.Observe(process.StartParams{Slug: "app", JobRunID: 7, JobSchedule: "refresh"}, process.RunHandle{PID: 10}, "")
	delete(readings, 10)
	end()
	end() // completion hook is idempotent
	exp := c.exporter.(*recordingExporter)
	exp.fail = true
	b := c.collect()
	if len(b.resources) != 1 {
		t.Fatalf("short run resources = %d", len(b.resources))
	}
	if err := c.export(context.Background(), b); err == nil {
		t.Fatal("want export failure")
	}
	if len(c.completed) != 1 {
		t.Fatal("lost short run during outage")
	}
	exp.fail = false
	if err := c.export(context.Background(), c.collect()); err != nil {
		t.Fatal(err)
	}
	if len(c.collect().resources) != 0 {
		t.Fatal("completed series continues to emit")
	}
}

func TestIdentityAndReplicaZeroEdge(t *testing.T) {
	c, _ := testCollector()
	end := c.Observe(process.StartParams{Slug: "app", Index: 2, DeploymentID: 9,
		Env:       []string{"PASSWORD=not-exported", "OTEL_RESOURCE_ATTRIBUTES=team=data%2Cscience,shinyhub.replica=999,shinyhub.schedule=wrong"},
		SecretEnv: []string{"OTEL_RESOURCE_ATTRIBUTES=team=override%20team"}}, process.RunHandle{PID: 10}, "")
	b := c.collect()
	for _, rm := range b.resources {
		if rm.ScopeMetrics[0].Metrics[0].Name == "shinyhub.replicas" {
			continue
		}
		attrs := map[string]string{}
		for _, kv := range rm.Resource.Attributes {
			attrs[kv.Key] = kv.Value.GetStringValue()
		}
		if attrs["shinyhub.replica"] != "2" || attrs["shinyhub.deployment.id"] != "9" || attrs["team"] != "override team" || attrs["shinyhub.schedule"] != "" || attrs["PASSWORD"] != "" || attrs["service.instance.id"] == "" {
			t.Fatalf("attrs = %v", attrs)
		}
	}
	if count := metricsNamed(b, "shinyhub.replicas")[0].GetGauge().DataPoints[0].GetAsInt(); count != 1 {
		t.Fatalf("replicas = %d", count)
	}
	end()
	b = c.collect()
	if count := metricsNamed(b, "shinyhub.replicas")[0].GetGauge().DataPoints[0].GetAsInt(); count != 0 {
		t.Fatalf("replicas after exit = %d", count)
	}
	if err := c.export(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if len(c.collect().resources) != 0 {
		t.Fatal("zero edge or exited workload retained after success")
	}
}

func TestNativeShortScheduledRunEmitsWithoutPolling(t *testing.T) {
	c, _ := testCollector()
	c.sampler = newSampler()
	rt := process.NewNativeRuntime()
	rt.SetWorkloadObserver(c.Observe)
	info, err := rt.RunOnce(context.Background(), process.StartParams{Slug: "app", JobRunID: 8, JobSchedule: "refresh", Dir: t.TempDir(), Command: []string{"/bin/sh", "-c", "sleep 0.1"}}, io.Discard)
	if err != nil || info.Code != 0 {
		t.Fatalf("run = %v, %v", info, err)
	}
	if len(c.active) != 0 || len(c.completed) != 1 {
		t.Fatalf("active = %d, completed = %d", len(c.active), len(c.completed))
	}
	b := c.collect()
	if len(metricsNamed(b, "shinyhub.process.memory.usage")) != 1 || len(metricsNamed(b, "shinyhub.process.cpu.time")) != 1 {
		t.Fatal("missing short-run observations")
	}
}

func TestCompletionQueueBounded(t *testing.T) {
	c, _ := testCollector()
	// Seed completed observations to simulate a collector outage without
	// thousands of launches. Completing one more run must evict only the oldest.
	for i := 0; i < maxCompleted; i++ {
		c.completed = append(c.completed, &metricspb.ResourceMetrics{})
	}
	oldest := c.completed[0]
	c.Observe(process.StartParams{Slug: "app", JobRunID: 1}, process.RunHandle{PID: 10}, "")()
	if len(c.completed) != maxCompleted || c.completed[0] == oldest {
		t.Fatal("completion queue is unbounded or did not evict oldest")
	}
}

func TestPartialReadsDoNotUnderreportRSSOrDoubleCountCPU(t *testing.T) {
	c, readings := testCollector()
	end := c.Observe(process.StartParams{Slug: "app"}, process.RunHandle{PID: 10}, "")
	defer end()
	readings[11] = reading{created: 1000001, cpu: 3, rss: 20}
	c.collect()
	read := c.sampler.process
	c.sampler.process = func(pid int32) (reading, error) {
		if pid == 11 {
			return reading{}, errors.New("permission")
		}
		return read(pid)
	}
	c.now = func() time.Time { return time.Unix(1001, 0) }
	b := c.collect()
	if len(metricsNamed(b, "shinyhub.process.memory.usage")) != 0 {
		t.Fatal("published partial RSS as complete")
	}
	c.sampler.process = read
	c.sampler.groups = func() (map[int32][]int32, error) { return map[int32][]int32{10: {10}}, nil }
	c.collect() // transient membership omission of a still-living child
	c.sampler.groups = func() (map[int32][]int32, error) { return map[int32][]int32{10: {10, 11}}, nil }
	b = c.collect()
	if got := metricsNamed(b, "shinyhub.process.cpu.time")[0].GetSum().DataPoints[0].GetAsDouble(); got != 3 {
		t.Fatalf("double-counted child CPU: %v", got)
	}
}

func TestRecoveryDoesNotBillHistoricalWorkerCPU(t *testing.T) {
	c, readings := testCollector()
	end := c.Observe(process.StartParams{Slug: "app"}, process.RunHandle{PID: 10}, "")
	defer end()
	readings[11] = reading{created: 99, cpu: 300, rss: 20}
	c.collect()
	readings[11] = reading{created: 99, cpu: 302, rss: 20}
	b := c.collect()
	if got := metricsNamed(b, "shinyhub.process.cpu.time")[0].GetSum().DataPoints[0].GetAsDouble(); got != 2 {
		t.Fatalf("recovery billed pre-registration CPU: %v", got)
	}
}

func TestQueuedBatchDoesNotReplayAcknowledgedCompletion(t *testing.T) {
	c, _ := testCollector()
	c.Observe(process.StartParams{Slug: "app", JobRunID: 1}, process.RunHandle{PID: 10}, "")()
	first, second := c.collect(), c.collect()
	if err := c.export(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := c.export(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if got := len(c.exporter.(*recordingExporter).batches); got != 1 {
		t.Fatalf("completion exported %d times", got)
	}
}

type blockedExporter struct {
	entered chan struct{}
	once    sync.Once
}

func (e *blockedExporter) Export(ctx context.Context, _ []*metricspb.ResourceMetrics) error {
	e.once.Do(func() { close(e.entered) })
	<-ctx.Done()
	return ctx.Err()
}
func (*blockedExporter) Close() error { return nil }

func TestSamplingContinuesDuringBlockedExportAndShutdownCancelsIt(t *testing.T) {
	c, _ := testCollector()
	c.interval = 10 * time.Millisecond
	read := c.sampler.process
	var samples atomic.Int32
	sampled := make(chan struct{})
	c.sampler.process = func(pid int32) (reading, error) {
		if samples.Add(1) == 6 {
			close(sampled)
		}
		return read(pid)
	}
	end := c.Observe(process.StartParams{Slug: "app"}, process.RunHandle{PID: 10}, "")
	defer end()
	exp := &blockedExporter{entered: make(chan struct{})}
	c.exporter = exp
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	select {
	case <-exp.entered:
	case <-time.After(time.Second):
		t.Fatal("export never started")
	}
	select {
	case <-sampled:
	case <-time.After(time.Second):
		t.Fatal("network request stopped sampling")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel export")
	}
}

func TestLiveChildrenStillSampledAfterGroupLeaderExits(t *testing.T) {
	c, readings := testCollector()
	end := c.Observe(process.StartParams{Slug: "app"}, process.RunHandle{PID: 10}, "")
	defer end()
	readings[11] = reading{created: 1000001, cpu: 3, rss: 20}
	delete(readings, 10)
	b := c.collect()
	if got := metricsNamed(b, "shinyhub.process.memory.usage")[0].GetGauge().DataPoints[0].GetAsInt(); got != 20 {
		t.Fatalf("live child RSS = %d", got)
	}
}

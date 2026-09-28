package jobs_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/jobs"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/secrets"
	"github.com/rvben/shinyhub/internal/spanerr"
)

// enabledTracing is a tracing config that makes JobEnvFor emit the full
// OTEL_* default set.
var enabledTracing = config.TracingConfig{
	Enabled:      true,
	OTLPEndpoint: "http://c:4318",
	OTLPProtocol: "http/protobuf",
	SampleRatio:  1,
}

// foreignTraceparent is a valid W3C traceparent that is not the run's own, so
// a test can tell a per-app value from the run span's context.
const foreignTraceparent = "00-11111111111111111111111111111111-2222222222222222-01"

func newJobsTracer(t *testing.T) (trace.Tracer, *tracetest.SpanRecorder) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return tp.Tracer("test"), rec
}

// newJobManager builds a manager over store with a runtime and data dirs of
// its own, the same construction the other manager tests use.
func newJobManager(t *testing.T, rt process.Runtime, store jobs.Store, key []byte) *jobs.Manager {
	t.Helper()
	dir := t.TempDir()
	m, err := jobs.NewManager(process.NewManager(dir, rt), nil, process.DefaultTier, store, key, dir, dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

type jobFixture struct {
	m     *jobs.Manager
	rt    *fakeRuntime
	st    *fakeStore
	sched *db.Schedule
	app   *db.App
}

func newJobFixture(t *testing.T, policy string, rt *fakeRuntime) jobFixture {
	t.Helper()
	sched, app := makeSchedule(policy, 30), makeApp()
	st := newFakeStore(sched, app)
	return jobFixture{m: newJobManager(t, rt, st, nil), rt: rt, st: st, sched: sched, app: app}
}

// lastEnv returns the value of the last KEY= entry in env, the one a process
// sees under last-occurrence-wins semantics; "" when key is absent.
func lastEnv(env []string, key string) string {
	val := ""
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			val = v
		}
	}
	return val
}

func envValues(env []string, key string) []string {
	var out []string
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			out = append(out, v)
		}
	}
	return out
}

func attrMap(kvs []attribute.KeyValue) map[string]string {
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		out[string(kv.Key)] = kv.Value.Emit()
	}
	return out
}

func lastParams(rt *fakeRuntime) process.StartParams {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.lastParams
}

// effectiveEnv is the env a job process sees: runtimes append SecretEnv after
// Env (process/native.go, process/docker.go).
func effectiveEnv(p process.StartParams) []string {
	return append(append([]string{}, p.Env...), p.SecretEnv...)
}

// waitEnded waits until rec holds at least n ended spans and returns them.
func waitEnded(t *testing.T, rec *tracetest.SpanRecorder, n int) []sdktrace.ReadOnlySpan {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if spans := rec.Ended(); len(spans) >= n {
			return spans
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("wanted %d ended spans, got %d", n, len(rec.Ended()))
	return nil
}

// waitRowFinished waits until runID's row is terminal and returns its status.
func waitRowFinished(t *testing.T, st *fakeStore, runID int64) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		r := st.runs[runID]
		done := r != nil && r.FinishedAt != nil
		status := ""
		if r != nil {
			status = r.Status
		}
		st.mu.Unlock()
		if done {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run %d never reached a terminal row", runID)
	return ""
}

// waitNoOpenSpans waits for the manager's per-run span map to drain.
func waitNoOpenSpans(t *testing.T, m *jobs.Manager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if jobs.RunSpanCount(m) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("runSpans still holds %d entries", jobs.RunSpanCount(m))
}

func runAndWait(t *testing.T, f jobFixture) int64 {
	t.Helper()
	runID, err := f.m.Run(f.sched.ID, "manual", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitRowFinished(t, f.st, runID)
	waitNoOpenSpans(t, f.m)
	return runID
}

func waitEntered(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not enter the runtime")
	}
}

func TestJobRun_InjectsOTelEnvAndTraceparentOfRunSpan(t *testing.T) {
	f := newJobFixture(t, "concurrent", &fakeRuntime{exitInfo: process.ExitInfo{Code: 0}})
	f.sched.Name = "nightly refresh"
	tr, rec := newJobsTracer(t)
	f.m.SetTracing(enabledTracing, tr)

	runID := runAndWait(t, f)
	env := lastParams(f.rt).Env
	if lastEnv(env, "OTEL_SERVICE_NAME") != f.app.Slug {
		t.Fatalf("service name: %v", env)
	}
	ra := lastEnv(env, "OTEL_RESOURCE_ATTRIBUTES")
	if !strings.Contains(ra, "shinyhub.schedule="+url.PathEscape(f.sched.Name)) ||
		!strings.Contains(ra, fmt.Sprintf("shinyhub.schedule.run_id=%d", runID)) {
		t.Fatalf("resource attrs: %q", ra)
	}
	spans := waitEnded(t, rec, 1)
	if len(spans) != 1 || spans[0].Name() != "schedule.run" {
		t.Fatalf("spans: %v", spans)
	}
	if spans[0].Parent().IsValid() {
		t.Fatalf("schedule.run must be a root span, parent %v", spans[0].Parent())
	}
	sc := spans[0].SpanContext()
	want := fmt.Sprintf("00-%s-%s-01", sc.TraceID(), sc.SpanID())
	if got := lastEnv(env, "TRACEPARENT"); got != want {
		t.Fatalf("TRACEPARENT %q want %q", got, want)
	}
	attrs := attrMap(spans[0].Attributes())
	wantAttrs := map[string]string{
		"shinyhub.app.slug":           f.app.Slug,
		"shinyhub.schedule.name":      f.sched.Name,
		"shinyhub.schedule.id":        fmt.Sprint(f.sched.ID),
		"shinyhub.schedule.run_id":    fmt.Sprint(runID),
		"shinyhub.schedule.trigger":   "manual",
		"shinyhub.deployment.id":      "1",
		"shinyhub.schedule.status":    "succeeded",
		"shinyhub.schedule.persisted": "true",
		"process.exit.code":           "0",
	}
	for k, v := range wantAttrs {
		if attrs[k] != v {
			t.Fatalf("attr %s=%q want %q (all: %v)", k, attrs[k], v, attrs)
		}
	}
	if st := spans[0].Status(); st.Code == codes.Error {
		t.Fatalf("succeeded run span status %v", st)
	}
}

func TestJobRun_PerAppOTelEnvWinsOverPlatform(t *testing.T) {
	f := newJobFixture(t, "concurrent", &fakeRuntime{exitInfo: process.ExitInfo{Code: 0}})
	f.st.envVars = []db.AppEnvVar{{Key: "OTEL_SERVICE_NAME", Value: []byte("custom")}}
	tr, _ := newJobsTracer(t)
	f.m.SetTracing(enabledTracing, tr)

	runAndWait(t, f)
	env := lastParams(f.rt).Env
	if got := envValues(env, "OTEL_SERVICE_NAME"); len(got) != 2 || got[0] != f.app.Slug {
		t.Fatalf("want the platform default followed by the per-app value, got %v", got)
	}
	if got := lastEnv(env, "OTEL_SERVICE_NAME"); got != "custom" {
		t.Fatalf("effective OTEL_SERVICE_NAME %q, want the per-app value", got)
	}
}

func TestJobRun_RunTraceparentBeatsPerAppValues(t *testing.T) {
	key := secrets.DeriveKey("test-auth-secret")
	encrypt := func(v string) []byte {
		b, err := secrets.Encrypt(key, []byte(v))
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		return b
	}
	cases := map[string][]db.AppEnvVar{
		"plain": {
			{Key: "TRACEPARENT", Value: []byte(foreignTraceparent)},
			{Key: "TRACESTATE", Value: []byte("foreign=1")},
		},
		"secret": {
			{Key: "TRACEPARENT", Value: encrypt(foreignTraceparent), IsSecret: true},
			{Key: "TRACESTATE", Value: encrypt("foreign=1"), IsSecret: true},
		},
	}
	for name, vars := range cases {
		t.Run(name, func(t *testing.T) {
			rt := &fakeRuntime{exitInfo: process.ExitInfo{Code: 0}}
			sched, app := makeSchedule("concurrent", 30), makeApp()
			st := newFakeStore(sched, app)
			st.envVars = vars
			m := newJobManager(t, rt, st, key)
			tr, rec := newJobsTracer(t)
			m.SetTracing(enabledTracing, tr)

			runAndWait(t, jobFixture{m: m, rt: rt, st: st, sched: sched, app: app})
			spans := waitEnded(t, rec, 1)
			sc := spans[0].SpanContext()
			effective := effectiveEnv(lastParams(rt))
			want := fmt.Sprintf("00-%s-%s-01", sc.TraceID(), sc.SpanID())
			if got := lastEnv(effective, "TRACEPARENT"); got != want {
				t.Fatalf("effective TRACEPARENT %q, want the run span's %q (env %v)", got, want, effective)
			}
			if got := lastEnv(effective, "TRACESTATE"); got != "" {
				t.Fatalf("effective TRACESTATE %q, want empty (the run span carries no state)", got)
			}
		})
	}
}

// terminalStore wraps fakeStore so the terminal write fails on demand and
// records when it last succeeded.
type terminalStore struct {
	*fakeStore

	tmu       sync.Mutex
	failures  int // remaining failing calls; negative fails forever
	failErr   error
	successAt time.Time
	failed    chan struct{}
}

func (s *terminalStore) CompleteScheduleRunAndEnqueueActivation(p db.CompleteScheduleRunParams) (*db.ScheduleActivation, error) {
	s.tmu.Lock()
	fail := s.failures != 0
	if s.failures > 0 {
		s.failures--
	}
	s.tmu.Unlock()
	if fail {
		select {
		case s.failed <- struct{}{}:
		default:
		}
		return nil, s.failErr
	}
	a, err := s.fakeStore.CompleteScheduleRunAndEnqueueActivation(p)
	s.tmu.Lock()
	s.successAt = time.Now()
	s.tmu.Unlock()
	return a, err
}

func TestJobRun_SpanEndsAfterTerminalRowAndRecordsAbandonedWrite(t *testing.T) {
	setup := func(t *testing.T, failures int, failErr error) (*jobs.Manager, *terminalStore, *tracetest.SpanRecorder) {
		t.Helper()
		st := &terminalStore{
			fakeStore: newFakeStore(makeSchedule("concurrent", 30), makeApp()),
			failures:  failures,
			failErr:   failErr,
			failed:    make(chan struct{}, 1),
		}
		m := newJobManager(t, &fakeRuntime{exitInfo: process.ExitInfo{Code: 0}}, st, nil)
		tr, rec := newJobsTracer(t)
		m.SetTracing(enabledTracing, tr)
		if _, err := m.Run(1, "manual", nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return m, st, rec
	}
	assertNotPersisted := func(t *testing.T, m *jobs.Manager, rec *tracetest.SpanRecorder) {
		t.Helper()
		spans := waitEnded(t, rec, 1)
		waitNoOpenSpans(t, m)
		if len(spans) != 1 {
			t.Fatalf("spans: %v", spans)
		}
		if got := attrMap(spans[0].Attributes())["shinyhub.schedule.persisted"]; got != "false" {
			t.Fatalf("persisted=%q, want false", got)
		}
		if st := spans[0].Status(); st.Code != codes.Error || !strings.Contains(st.Description, "not persisted") {
			t.Fatalf("status %+v, want Error mentioning not persisted", st)
		}
	}

	t.Run("persisted", func(t *testing.T) {
		m, st, rec := setup(t, 2, fmt.Errorf("temporary store outage"))
		spans := waitEnded(t, rec, 1)
		waitNoOpenSpans(t, m)
		if len(spans) != 1 {
			t.Fatalf("spans: %v", spans)
		}
		if got := attrMap(spans[0].Attributes())["shinyhub.schedule.persisted"]; got != "true" {
			t.Fatalf("persisted=%q, want true", got)
		}
		var successAt time.Time
		for deadline := time.Now().Add(5 * time.Second); successAt.IsZero() && time.Now().Before(deadline); {
			st.tmu.Lock()
			successAt = st.successAt
			st.tmu.Unlock()
			if successAt.IsZero() {
				time.Sleep(5 * time.Millisecond)
			}
		}
		if successAt.IsZero() {
			t.Fatal("terminal write never succeeded")
		}
		if end := spans[0].EndTime(); end.Before(successAt) {
			t.Fatalf("span ended at %v, before the terminal row was written at %v", end, successAt)
		}
	})

	t.Run("abandoned", func(t *testing.T) {
		m, st, rec := setup(t, -1, fmt.Errorf("temporary store outage"))
		select {
		case <-st.failed:
		case <-time.After(5 * time.Second):
			t.Fatal("terminal write was never attempted")
		}
		// A shutdown whose caller has already given up cancels the terminal
		// context while finishRun is retrying.
		expired, cancel := context.WithCancel(context.Background())
		cancel()
		m.Stop(expired)
		assertNotPersisted(t, m, rec)
	})

	t.Run("row gone", func(t *testing.T) {
		m, _, rec := setup(t, -1, db.ErrNotFound)
		assertNotPersisted(t, m, rec)
	})
}

// An abandoned terminal write exports a reduced copy of the store error on the
// schedule.run span: a database error can carry a connection string and run
// long, and neither may leave the process on a span.
func TestJobRun_NotPersistedSpanErrorIsRedactedAndBounded(t *testing.T) {
	const secret = "tok-s3cret"
	st := &terminalStore{
		fakeStore: newFakeStore(makeSchedule("concurrent", 30), makeApp()),
		failures:  -1,
		failErr: fmt.Errorf("connect postgres://app:%s@db.example/shinyhub: %s\ndetail: %s",
			secret, strings.Repeat("x", 4000), secret),
		failed: make(chan struct{}, 1),
	}
	m := newJobManager(t, &fakeRuntime{exitInfo: process.ExitInfo{Code: 0}}, st, nil)
	tr, rec := newJobsTracer(t)
	m.SetTracing(enabledTracing, tr)
	if _, err := m.Run(1, "manual", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	select {
	case <-st.failed:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal write was never attempted")
	}
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	m.Stop(expired)

	spans := waitEnded(t, rec, 1)
	waitNoOpenSpans(t, m)
	desc := spans[0].Status().Description
	if spans[0].Status().Code != codes.Error || !strings.Contains(desc, "not persisted") {
		t.Fatalf("status code %v, want Error mentioning not persisted: %.120q", spans[0].Status().Code, desc)
	}
	if strings.Contains(desc, secret) {
		t.Errorf("secret exported (%d bytes): %.120q", len(desc), desc)
	}
	if strings.ContainsAny(desc, "\r\n") {
		t.Errorf("newline exported (%d bytes): %.120q", len(desc), desc)
	}
	if len(desc) > spanerr.MaxBytes {
		t.Errorf("exported %d bytes, want <= %d", len(desc), spanerr.MaxBytes)
	}
}

func TestJobRun_FailedExitMarksSpanError(t *testing.T) {
	f := newJobFixture(t, "concurrent", &fakeRuntime{exitInfo: process.ExitInfo{Code: 3}})
	tr, rec := newJobsTracer(t)
	f.m.SetTracing(enabledTracing, tr)

	runAndWait(t, f)
	spans := waitEnded(t, rec, 1)
	if len(spans) != 1 {
		t.Fatalf("spans: %v", spans)
	}
	attrs := attrMap(spans[0].Attributes())
	if attrs["process.exit.code"] != "3" || attrs["shinyhub.schedule.status"] != "failed" {
		t.Fatalf("attrs: %v", attrs)
	}
	if st := spans[0].Status(); st.Code != codes.Error {
		t.Fatalf("status %+v, want Error", st)
	}
}

func TestJobRun_LaunchErrorStillEndsSpan(t *testing.T) {
	assertFailedWithoutLaunch := func(t *testing.T, m *jobs.Manager, rec *tracetest.SpanRecorder) {
		t.Helper()
		spans := waitEnded(t, rec, 1)
		if len(spans) != 1 {
			t.Fatalf("spans: %v", spans)
		}
		attrs := attrMap(spans[0].Attributes())
		if attrs["shinyhub.schedule.status"] != "failed" || attrs["shinyhub.schedule.persisted"] != "true" {
			t.Fatalf("attrs: %v", attrs)
		}
		if _, ok := attrs["process.exit.code"]; ok {
			t.Fatalf("a run that never launched has no exit code: %v", attrs)
		}
		if st := spans[0].Status(); st.Code != codes.Error {
			t.Fatalf("status %+v, want Error", st)
		}
		if n := jobs.RunSpanCount(m); n != 0 {
			t.Fatalf("runSpans holds %d entries after the run ended", n)
		}
	}

	t.Run("runtime cannot start the command", func(t *testing.T) {
		f := newJobFixture(t, "concurrent", &fakeRuntime{err: fmt.Errorf(`exec: "python": executable file not found in $PATH`)})
		tr, rec := newJobsTracer(t)
		f.m.SetTracing(enabledTracing, tr)
		runAndWait(t, f)
		assertFailedWithoutLaunch(t, f.m, rec)
	})

	t.Run("secret env cannot be resolved", func(t *testing.T) {
		rt := &fakeRuntime{exitInfo: process.ExitInfo{Code: 0}}
		sched, app := makeSchedule("concurrent", 30), makeApp()
		st := newFakeStore(sched, app)
		st.envVars = []db.AppEnvVar{{Key: "API_TOKEN", Value: []byte("not ciphertext"), IsSecret: true}}
		m := newJobManager(t, rt, st, secrets.DeriveKey("test-auth-secret"))
		tr, rec := newJobsTracer(t)
		m.SetTracing(enabledTracing, tr)
		runAndWait(t, jobFixture{m: m, rt: rt, st: st, sched: sched, app: app})
		rt.mu.Lock()
		calls := len(rt.deadlinesAtEntry)
		rt.mu.Unlock()
		if calls != 0 {
			t.Fatalf("RunOnce entered %d times, want 0", calls)
		}
		assertFailedWithoutLaunch(t, m, rec)
	})
}

func TestJobRun_TracingDisabledInjectsNothing(t *testing.T) {
	cases := map[string]func(tr trace.Tracer) (config.TracingConfig, trace.Tracer){
		"disabled config": func(tr trace.Tracer) (config.TracingConfig, trace.Tracer) { return config.TracingConfig{}, tr },
		"nil tracer":      func(trace.Tracer) (config.TracingConfig, trace.Tracer) { return enabledTracing, nil },
	}
	for name, pick := range cases {
		t.Run(name, func(t *testing.T) {
			f := newJobFixture(t, "concurrent", &fakeRuntime{exitInfo: process.ExitInfo{Code: 0}})
			f.st.envVars = []db.AppEnvVar{{Key: "TRACEPARENT", Value: []byte(foreignTraceparent)}}
			tr, rec := newJobsTracer(t)
			f.m.SetTracing(pick(tr))

			runAndWait(t, f)
			env := effectiveEnv(lastParams(f.rt))
			for _, kv := range env {
				if strings.HasPrefix(kv, "OTEL_") || strings.HasPrefix(kv, "TRACESTATE=") {
					t.Fatalf("tracing off injected %q (env %v)", kv, env)
				}
			}
			// A per-app TRACEPARENT reaches the job unchanged, exactly as before.
			if got := envValues(env, "TRACEPARENT"); len(got) != 1 || got[0] != foreignTraceparent {
				t.Fatalf("TRACEPARENT entries %v, want only the per-app value", got)
			}
			if n := len(rec.Ended()) + len(rec.Started()); n != 0 {
				t.Fatalf("tracing off recorded %d spans", n)
			}
		})
	}
}

func TestJobRun_SampledOutRunPropagatesUnsampledContext(t *testing.T) {
	f := newJobFixture(t, "concurrent", &fakeRuntime{exitInfo: process.ExitInfo{Code: 0}})
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()), sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	f.m.SetTracing(enabledTracing, tp.Tracer("test"))

	runAndWait(t, f)
	env := lastParams(f.rt).Env
	// The run still carries a valid context so job spans join the same
	// (unsampled) trace and honour the decision instead of starting a
	// fresh sampled root.
	tp0 := lastEnv(env, "TRACEPARENT")
	parts := strings.Split(tp0, "-")
	if len(parts) != 4 || parts[0] != "00" || len(parts[1]) != 32 || len(parts[2]) != 16 || parts[3] != "00" {
		t.Fatalf("TRACEPARENT %q, want a valid unsampled (flags 00) context", tp0)
	}
	if parts[1] == strings.Repeat("0", 32) || parts[2] == strings.Repeat("0", 16) {
		t.Fatalf("TRACEPARENT %q carries an invalid zero id", tp0)
	}
	if n := len(rec.Started()) + len(rec.Ended()); n != 0 {
		t.Fatalf("sampled-out run recorded %d spans, want 0", n)
	}
}

func TestJobRun_CancelledWhileQueuedStillEndsSpan(t *testing.T) {
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	f := newJobFixture(t, "queue", &fakeRuntime{exitInfo: process.ExitInfo{Code: 0}, block: block, entered: entered})
	tr, rec := newJobsTracer(t)
	f.m.SetTracing(enabledTracing, tr)

	idA, err := f.m.Run(1, "cron", nil)
	if err != nil {
		t.Fatalf("Run A: %v", err)
	}
	waitEntered(t, entered)
	idB, err := f.m.Run(1, "cron", nil)
	if err != nil {
		t.Fatalf("Run B: %v", err)
	}
	if err := f.m.Cancel(idB); err != nil {
		t.Fatalf("Cancel B: %v", err)
	}
	statusB := waitRowFinished(t, f.st, idB)
	spans := waitEnded(t, rec, 1)
	if len(spans) != 1 {
		t.Fatalf("spans while A still runs: %v", spans)
	}
	attrs := attrMap(spans[0].Attributes())
	if attrs["shinyhub.schedule.run_id"] != fmt.Sprint(idB) || attrs["shinyhub.schedule.status"] != statusB || statusB != "cancelled" {
		t.Fatalf("queued span attrs %v, row status %q", attrs, statusB)
	}
	if st := spans[0].Status(); st.Code != codes.Error {
		t.Fatalf("status %+v, want Error", st)
	}

	close(block)
	waitRowFinished(t, f.st, idA)
	if got := len(waitEnded(t, rec, 2)); got != 2 {
		t.Fatalf("ended spans %d, want 2", got)
	}
	waitNoOpenSpans(t, f.m)
}

func TestJobRun_SkippedOverlapHasNoSpan(t *testing.T) {
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	f := newJobFixture(t, "skip", &fakeRuntime{exitInfo: process.ExitInfo{Code: 0}, block: block, entered: entered})
	tr, rec := newJobsTracer(t)
	f.m.SetTracing(enabledTracing, tr)

	idA, err := f.m.Run(1, "cron", nil)
	if err != nil {
		t.Fatalf("Run A: %v", err)
	}
	waitEntered(t, entered)
	idSkipped, err := f.m.Run(1, "cron", nil)
	if err != nil {
		t.Fatalf("Run skipped: %v", err)
	}
	if status := waitRowFinished(t, f.st, idSkipped); status != "skipped_overlap" {
		t.Fatalf("second run status %q, want skipped_overlap", status)
	}
	if n := len(rec.Started()); n != 1 {
		t.Fatalf("started spans %d, want 1 (only A)", n)
	}
	if n := jobs.RunSpanCount(f.m); n != 1 {
		t.Fatalf("open run spans %d, want 1 (only A)", n)
	}

	close(block)
	waitRowFinished(t, f.st, idA)
	waitNoOpenSpans(t, f.m)
	spans := waitEnded(t, rec, 1)
	if len(spans) != 1 || attrMap(spans[0].Attributes())["shinyhub.schedule.run_id"] != fmt.Sprint(idA) {
		t.Fatalf("spans: %v", spans)
	}
}

func TestJobRun_DeployTriggeredRunsAreTraced(t *testing.T) {
	t.Run("deploy obligation", func(t *testing.T) {
		f := newJobFixture(t, "concurrent", &fakeRuntime{exitInfo: process.ExitInfo{Code: 0}})
		tr, rec := newJobsTracer(t)
		f.m.SetTracing(enabledTracing, tr)
		f.st.deployments[0].ContentDigest = "sha256:v1"
		runID, err := f.m.RunDeployObligation(&db.ScheduleDeployObligation{
			ID: 9, ScheduleID: f.sched.ID, DeploymentID: f.st.deployments[0].ID,
			AppVersion: "v1", ContentDigest: "sha256:v1",
			ProducerFingerprint: "fingerprint", ProducerCommandJSON: f.sched.CommandJSON,
			TimeoutSeconds: 30, OnSuccess: "none", RollFallback: "defer", Status: "dispatching",
		})
		if err != nil {
			t.Fatalf("RunDeployObligation: %v", err)
		}
		waitEnded(t, rec, 1)
		waitNoOpenSpans(t, f.m)
		spans := rec.Ended()
		if len(spans) != 1 {
			t.Fatalf("deploy obligation ended %d spans, want 1", len(spans))
		}
		attrs := attrMap(spans[0].Attributes())
		if attrs["shinyhub.schedule.trigger"] != "deploy" || attrs["shinyhub.schedule.run_id"] != fmt.Sprint(runID) {
			t.Fatalf("attrs: %v", attrs)
		}
		if lastEnv(lastParams(f.rt).Env, "TRACEPARENT") == "" {
			t.Fatal("deploy obligation run did not receive TRACEPARENT")
		}
	})

	t.Run("candidate producer", func(t *testing.T) {
		f := newJobFixture(t, "concurrent", &fakeRuntime{exitInfo: process.ExitInfo{Code: 0}})
		f.sched.DeployTrigger = "bundle_change"
		tr, rec := newJobsTracer(t)
		f.m.SetTracing(enabledTracing, tr)
		pending := &db.Deployment{ID: 9, AppID: f.app.ID, Version: "v2", BundleDir: t.TempDir(), ContentDigest: "sha256:v2", Status: db.DeploymentPending}
		release := f.m.AcquireProducerGates([]int64{f.sched.ID})
		runID, err := f.m.RunCandidateProducerLocked(f.sched, f.app, pending)
		release()
		if err != nil {
			t.Fatalf("RunCandidateProducerLocked: %v", err)
		}
		spans := rec.Ended()
		if len(spans) != 1 {
			t.Fatalf("candidate producer returned with %d ended spans, want 1", len(spans))
		}
		attrs := attrMap(spans[0].Attributes())
		if attrs["shinyhub.schedule.trigger"] != "deploy" || attrs["shinyhub.schedule.run_id"] != fmt.Sprint(runID) || attrs["shinyhub.deployment.id"] != "9" {
			t.Fatalf("attrs: %v", attrs)
		}
		if n := jobs.RunSpanCount(f.m); n != 0 {
			t.Fatalf("runSpans holds %d entries", n)
		}
	})
}

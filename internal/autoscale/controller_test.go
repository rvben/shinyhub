package autoscale

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/proxy"
)

type fakeLister struct{ apps []*db.App }

func (f *fakeLister) ListAutoscaleApps() ([]*db.App, error) { return f.apps, nil }

type fakeSignal struct {
	counts  map[string][]int64
	rejects map[string]map[proxy.RejectReason]uint64
}

func (f *fakeSignal) ReplicaSessionCounts(slug string) []int64 { return f.counts[slug] }
func (f *fakeSignal) RejectsByReason(slug string, _ time.Duration) map[proxy.RejectReason]uint64 {
	return f.rejects[slug]
}

type fakeScaler struct {
	ups   map[string]int
	downs map[string]int
	// upNoOp / downNoOp make the primitive return (false, nil) - the benign
	// no-op the real primitives return at a ceiling/floor or for a non-running
	// app - so tests can assert the controller does not arm a cooldown on it.
	upNoOp   bool
	downNoOp bool
	// onUp, when set, is called after each ScaleUp call with the running call
	// count for this slug, so a test can observe cooldown state mid-loop.
	onUp func(callNum int)
}

func newFakeScaler() *fakeScaler {
	return &fakeScaler{ups: map[string]int{}, downs: map[string]int{}}
}
func (f *fakeScaler) ScaleUp(slug string) (bool, error) {
	f.ups[slug]++
	if f.onUp != nil {
		f.onUp(f.ups[slug])
	}
	return !f.upNoOp, nil
}
func (f *fakeScaler) ScaleDown(slug string, _ time.Duration) (bool, error) {
	f.downs[slug]++
	return !f.downNoOp, nil
}

// fakeCooldownStore is an in-memory CooldownStore. When seeded over the lister's
// app pointers (via cooldownStoreFor) a write reflects into the app so the next
// tick's reconcile reads the persisted value - simulating the DB round-trip.
type fakeCooldownStore struct {
	apps   map[string]*db.App
	writes int
	setErr error // when non-nil, SetAppLastAutoscaleAt returns it (write still counted)
}

func (f *fakeCooldownStore) SetAppLastAutoscaleAt(slug string, epoch int64) error {
	f.writes++
	if f.setErr != nil {
		return f.setErr
	}
	if a, ok := f.apps[slug]; ok {
		a.LastAutoscaleAt = epoch
	}
	return nil
}

// NowEpoch satisfies CooldownStore. The cooldown tests drive c.reconcile(now)
// directly with a controlled time, so this is only exercised if Run is used.
func (f *fakeCooldownStore) NowEpoch() (int64, error) { return time.Now().Unix(), nil }

func cooldownStoreFor(apps []*db.App) *fakeCooldownStore {
	m := make(map[string]*db.App, len(apps))
	for _, a := range apps {
		m[a.Slug] = a
	}
	return &fakeCooldownStore{apps: m}
}

func testCfg() Config {
	return Config{
		ScanInterval:  30 * time.Second,
		Cooldown:      3 * time.Minute,
		DrainGrace:    30 * time.Second,
		RejectWindow:  time.Minute,
		DefaultTarget: 0.8,
		DefaultCap:    10,
		RuntimeMax:    32,
	}
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newTestControllerCD builds a controller whose cooldown store is seeded over the
// lister's app pointers (so persisted cooldowns reflect into the next tick) and
// returns the store for inspection.
func newTestControllerCD(lister *fakeLister, signal Signal, scaler Scaler, rec AuditRecorder) (*Controller, *fakeCooldownStore) {
	cd := cooldownStoreFor(lister.apps)
	return New(testCfg(), lister, signal, scaler, rec, cd, testLogger()), cd
}

func newTestController(lister Lister, signal Signal, scaler Scaler, rec AuditRecorder) *Controller {
	c, _ := newTestControllerCD(lister.(*fakeLister), signal, scaler, rec)
	return c
}

func testController(lister Lister, signal Signal, scaler Scaler) *Controller {
	return newTestController(lister, signal, scaler, &fakeAuditor{})
}

func app(slug string, replicas, min, max int) *db.App {
	return &db.App{
		Slug: slug, Status: "running", Replicas: replicas,
		AutoscaleEnabled: true, AutoscaleMinReplicas: min, AutoscaleMaxReplicas: max,
	}
}

func TestController_ScalesUpToDesiredInOneTick(t *testing.T) {
	lister := &fakeLister{apps: []*db.App{app("demo", 2, 1, 8)}}
	signal := &fakeSignal{counts: map[string][]int64{"demo": {20, 20}}} // total 40 -> ceil(40/8)=5
	scaler := newFakeScaler()
	c := testController(lister, signal, scaler)

	c.reconcile(time.Now())

	if scaler.ups["demo"] != 3 { // 2 -> 5
		t.Fatalf("ScaleUp calls = %d, want 3", scaler.ups["demo"])
	}
	if scaler.downs["demo"] != 0 {
		t.Fatalf("unexpected ScaleDown calls = %d", scaler.downs["demo"])
	}
}

func TestController_ScalesDownOneStepPerTick(t *testing.T) {
	lister := &fakeLister{apps: []*db.App{app("demo", 3, 1, 8)}}
	signal := &fakeSignal{counts: map[string][]int64{"demo": {1, 1, 1}}} // total 3 -> ceil(3/8)=1
	scaler := newFakeScaler()
	c := testController(lister, signal, scaler)

	c.reconcile(time.Now())

	if scaler.downs["demo"] != 1 {
		t.Fatalf("ScaleDown calls = %d, want exactly 1 (one step per tick)", scaler.downs["demo"])
	}
	if scaler.ups["demo"] != 0 {
		t.Fatalf("unexpected ScaleUp calls = %d", scaler.ups["demo"])
	}
}

func TestController_NoOpWhenAtDesired(t *testing.T) {
	lister := &fakeLister{apps: []*db.App{app("demo", 3, 1, 8)}}
	signal := &fakeSignal{counts: map[string][]int64{"demo": {8, 8, 1}}} // total 17 -> ceil(17/8)=3
	scaler := newFakeScaler()
	c := testController(lister, signal, scaler)

	c.reconcile(time.Now())

	if scaler.ups["demo"] != 0 || scaler.downs["demo"] != 0 {
		t.Fatalf("expected no scale actions, got up=%d down=%d", scaler.ups["demo"], scaler.downs["demo"])
	}
}

func TestController_CooldownBlocksSecondAction(t *testing.T) {
	lister := &fakeLister{apps: []*db.App{app("demo", 2, 1, 8)}}
	signal := &fakeSignal{counts: map[string][]int64{"demo": {20, 20}}}
	scaler := newFakeScaler()
	c := testController(lister, signal, scaler)

	start := time.Now()
	c.reconcile(start)
	first := scaler.ups["demo"]
	if first == 0 {
		t.Fatalf("expected an initial scale-up")
	}
	// Within the cooldown window: no further actions.
	c.reconcile(start.Add(time.Minute))
	if scaler.ups["demo"] != first {
		t.Fatalf("cooldown not honoured: ups went %d -> %d", first, scaler.ups["demo"])
	}
	// After the cooldown elapses, action resumes.
	c.reconcile(start.Add(4 * time.Minute))
	if scaler.ups["demo"] <= first {
		t.Fatalf("expected scaling to resume after cooldown, ups still %d", scaler.ups["demo"])
	}
}

func TestController_HonoursPersistedCooldownOnFreshController(t *testing.T) {
	// A new active (fresh process, empty in-memory state) must honour a cooldown a
	// prior owner persisted: the value rides on the listed app, not process memory.
	now := time.Now()
	a := app("demo", 2, 1, 8)
	a.LastAutoscaleAt = now.Unix() // as if a prior owner just scaled it
	lister := &fakeLister{apps: []*db.App{a}}
	signal := &fakeSignal{counts: map[string][]int64{"demo": {20, 20}}} // would scale up if not for cooldown
	scaler := newFakeScaler()
	c, _ := newTestControllerCD(lister, signal, scaler, &fakeAuditor{})

	c.reconcile(now.Add(time.Minute)) // within the 3-minute cooldown
	if scaler.ups["demo"] != 0 {
		t.Fatalf("fresh controller ignored a persisted cooldown: ups=%d", scaler.ups["demo"])
	}
}

func TestCooldownSeconds(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want int64
	}{
		{0, 0},                               // disabled -> no throttle
		{-5 * time.Second, 0},                // defensive: negative -> no throttle
		{1 * time.Nanosecond, 1},             // never silently disabled
		{999 * time.Millisecond, 1},          // rounds up
		{1 * time.Second, 1},                 // exact
		{1*time.Second + time.Nanosecond, 2}, // rounds up past a whole second
		{3 * time.Minute, 180},               // typical value, exact
	}
	for _, c := range cases {
		if got := cooldownSeconds(c.in); got != c.want {
			t.Errorf("cooldownSeconds(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestController_CooldownPersistErrorIsNonFatal(t *testing.T) {
	// A failure to persist the cooldown must be logged, not fatal: the scale action
	// still takes effect and the audit event still records (worst case is a possible
	// duplicate action next tick - no worse than the old in-memory map on restart).
	lister := &fakeLister{apps: []*db.App{app("demo", 2, 1, 8)}}
	signal := &fakeSignal{counts: map[string][]int64{"demo": {20, 20}}} // total 40 -> desired 5 (up)
	scaler := newFakeScaler()
	auditor := &fakeAuditor{}
	cd := cooldownStoreFor(lister.apps)
	cd.setErr = errors.New("db write failed")
	c := New(testCfg(), lister, signal, scaler, auditor, cd, testLogger())

	c.reconcile(time.Now()) // must not panic

	if scaler.ups["demo"] == 0 {
		t.Fatal("a cooldown persist error must not prevent the scale action")
	}
	if len(auditor.events) != 1 {
		t.Fatalf("audit events = %d, want 1 (persist error is non-fatal)", len(auditor.events))
	}
	if cd.writes == 0 {
		t.Fatal("expected a persist attempt even though it errored")
	}
}

func TestController_ArmsCooldownOnFirstScaleUpStep(t *testing.T) {
	// A multi-step scale-up must arm (persist) the cooldown after the FIRST step,
	// not after the whole loop, so a crash mid-loop still leaves it set.
	lister := &fakeLister{apps: []*db.App{app("demo", 2, 1, 8)}}
	signal := &fakeSignal{counts: map[string][]int64{"demo": {20, 20}}} // total 40 -> desired 5 (3 steps)
	scaler := newFakeScaler()
	cd := cooldownStoreFor(lister.apps)
	var armedBeforeSecondStep bool
	scaler.onUp = func(callNum int) {
		// By the second ScaleUp call the cooldown must already be persisted.
		if callNum >= 2 && cd.writes >= 1 {
			armedBeforeSecondStep = true
		}
	}
	c := New(testCfg(), lister, signal, scaler, &fakeAuditor{}, cd, testLogger())

	c.reconcile(time.Now())

	if scaler.ups["demo"] != 3 {
		t.Fatalf("ScaleUp calls = %d, want 3 (2 -> 5)", scaler.ups["demo"])
	}
	if !armedBeforeSecondStep {
		t.Fatal("cooldown was not armed after the first successful step")
	}
	if cd.writes != 1 {
		t.Fatalf("cooldown writes = %d, want exactly 1 for one scale-up action", cd.writes)
	}
}

func TestController_SkipsAppUnknownToProxy(t *testing.T) {
	lister := &fakeLister{apps: []*db.App{app("demo", 2, 1, 8)}}
	signal := &fakeSignal{counts: map[string][]int64{}} // proxy has no pool for demo
	scaler := newFakeScaler()
	c := testController(lister, signal, scaler)

	c.reconcile(time.Now())

	if scaler.ups["demo"] != 0 || scaler.downs["demo"] != 0 {
		t.Fatalf("acted on app unknown to proxy: up=%d down=%d", scaler.ups["demo"], scaler.downs["demo"])
	}
}

func TestController_SkipsAppWithInvalidBounds(t *testing.T) {
	// An app flagged autoscale-enabled but with unset/zero bounds (e.g. a row
	// written outside the API's validated enable path) must never be acted on:
	// a zero max would otherwise clamp every decision to a single replica and
	// silently scale a healthy pool down.
	lister := &fakeLister{apps: []*db.App{app("demo", 4, 0, 0)}}
	signal := &fakeSignal{counts: map[string][]int64{"demo": {1, 1, 1, 1}}}
	scaler := newFakeScaler()
	c := testController(lister, signal, scaler)

	c.reconcile(time.Now())

	if scaler.ups["demo"] != 0 || scaler.downs["demo"] != 0 {
		t.Fatalf("acted on app with invalid bounds: up=%d down=%d", scaler.ups["demo"], scaler.downs["demo"])
	}
}

func TestController_SkipsAppWithEmptyPool(t *testing.T) {
	// A registered-but-empty pool yields a zero-length count slice, which carries
	// no usable signal. The controller must skip it rather than read it as "zero
	// load" and scale the recorded replicas down toward the floor.
	lister := &fakeLister{apps: []*db.App{app("demo", 3, 1, 8)}}
	signal := &fakeSignal{counts: map[string][]int64{"demo": {}}}
	scaler := newFakeScaler()
	c := testController(lister, signal, scaler)

	c.reconcile(time.Now())

	if scaler.ups["demo"] != 0 || scaler.downs["demo"] != 0 {
		t.Fatalf("acted on app with empty pool: up=%d down=%d", scaler.ups["demo"], scaler.downs["demo"])
	}
}

func TestController_NoOpScaleDownDoesNotArmCooldown(t *testing.T) {
	// A ScaleDown the primitive refuses (floor reached, app not running) must not
	// arm the cooldown, or the no-op would block a genuinely needed scale-up in
	// the opposite direction within the cooldown window.
	lister := &fakeLister{apps: []*db.App{app("demo", 2, 1, 8)}}
	signal := &fakeSignal{counts: map[string][]int64{"demo": {1, 1}}} // total 2 -> desired 1 (down)
	scaler := newFakeScaler()
	scaler.downNoOp = true
	c, cd := newTestControllerCD(lister, signal, scaler, &fakeAuditor{})

	start := time.Now()
	c.reconcile(start)
	if scaler.downs["demo"] != 1 {
		t.Fatalf("expected a ScaleDown attempt, got %d", scaler.downs["demo"])
	}
	if cd.writes != 0 {
		t.Fatalf("no-op scale-down armed the cooldown (%d writes)", cd.writes)
	}
	// Load spikes within the cooldown window; the prior no-op must not block it.
	signal.counts["demo"] = []int64{20, 20} // total 40 -> desired 5 (up)
	c.reconcile(start.Add(time.Second))
	if scaler.ups["demo"] == 0 {
		t.Fatalf("scale-up blocked by a cooldown that a no-op scale-down should not have armed")
	}
}

func TestController_NoOpScaleUpDoesNotArmCooldown(t *testing.T) {
	// A ScaleUp the primitive refuses must not arm the cooldown either; the next
	// tick should be free to try again rather than be suppressed for the window.
	lister := &fakeLister{apps: []*db.App{app("demo", 2, 1, 8)}}
	signal := &fakeSignal{counts: map[string][]int64{"demo": {20, 20}}} // desired 5 (up)
	scaler := newFakeScaler()
	scaler.upNoOp = true
	c, cd := newTestControllerCD(lister, signal, scaler, &fakeAuditor{})

	start := time.Now()
	c.reconcile(start)
	if cd.writes != 0 {
		t.Fatalf("no-op scale-up armed the cooldown (%d writes)", cd.writes)
	}
	c.reconcile(start.Add(time.Second))
	if scaler.ups["demo"] < 2 {
		t.Fatalf("expected a retry after a no-op scale-up, got %d attempts", scaler.ups["demo"])
	}
}

func TestController_DoesNotScaleDownDegradedPool(t *testing.T) {
	// A degraded pool reports a missing/nil replica slot (count -1) and is being
	// restored by the self-healer. ScaleDown removes the highest index and could
	// stop the last healthy replica before the slot heals, so the controller must
	// not scale such a pool down.
	lister := &fakeLister{apps: []*db.App{app("demo", 2, 1, 8)}}
	signal := &fakeSignal{counts: map[string][]int64{"demo": {-1, 0}}} // slot 0 missing
	scaler := newFakeScaler()
	c := testController(lister, signal, scaler)

	c.reconcile(time.Now())

	if scaler.downs["demo"] != 0 {
		t.Fatalf("scaled down a degraded pool: downs=%d", scaler.downs["demo"])
	}
}

func TestController_ScalesUpSaturatedDegradedPool(t *testing.T) {
	// Scale-up is still allowed on a degraded pool so a genuinely saturated app
	// gains capacity while the healer restores the missing slot; only scale-down
	// is withheld while the pool is not fully healthy.
	lister := &fakeLister{apps: []*db.App{app("demo", 2, 1, 8)}}
	signal := &fakeSignal{
		counts: map[string][]int64{"demo": {-1, 8}}, // slot 0 missing, slot 1 at cap
		rejects: map[string]map[proxy.RejectReason]uint64{
			"demo": {proxy.ReasonPoolSaturated: 3},
		},
	}
	scaler := newFakeScaler()
	c := testController(lister, signal, scaler)

	c.reconcile(time.Now())

	if scaler.ups["demo"] == 0 {
		t.Fatalf("expected scale-up on a saturated degraded pool")
	}
}

func TestController_SaturationBiasesScaleUp(t *testing.T) {
	lister := &fakeLister{apps: []*db.App{app("demo", 2, 1, 8)}}
	signal := &fakeSignal{
		counts: map[string][]int64{"demo": {8, 8}}, // total 16 -> ceil(16/8)=2 (== current)
		rejects: map[string]map[proxy.RejectReason]uint64{
			"demo": {proxy.ReasonPoolSaturated: 5},
		},
	}
	scaler := newFakeScaler()
	c := testController(lister, signal, scaler)

	c.reconcile(time.Now())

	if scaler.ups["demo"] != 1 {
		t.Fatalf("saturation should force one scale-up, got %d", scaler.ups["demo"])
	}
}

type fakeAuditor struct {
	events []db.AuditEventParams
}

func (f *fakeAuditor) LogAuditEvent(p db.AuditEventParams) {
	f.events = append(f.events, p)
}

func TestController_RecordsAuditEventOnScaleUp(t *testing.T) {
	lister := &fakeLister{apps: []*db.App{app("demo", 2, 1, 8)}}
	// ceil(40/8)=5; desired 5 > current 2 -> scale up.
	signal := &fakeSignal{counts: map[string][]int64{"demo": {20, 20}}}
	scaler := newFakeScaler()
	auditor := &fakeAuditor{}
	c := newTestController(lister, signal, scaler, auditor)

	c.reconcile(time.Now())

	if len(auditor.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(auditor.events))
	}
	ev := auditor.events[0]
	if ev.Action != ActionScaleUp {
		t.Fatalf("action = %q, want %q", ev.Action, ActionScaleUp)
	}
	if ev.ResourceType != "app" || ev.ResourceID != "demo" {
		t.Fatalf("resource = %q/%q, want app/demo", ev.ResourceType, ev.ResourceID)
	}
	if ev.UserID != nil {
		t.Fatalf("UserID = %v, want nil (system actor)", ev.UserID)
	}
	// Detail must be valid JSON with from/to/reason/sessions/target fields.
	var detail map[string]any
	if err := json.Unmarshal([]byte(ev.Detail), &detail); err != nil {
		t.Fatalf("detail JSON: %v", err)
	}
	if int(detail["from"].(float64)) != 2 {
		t.Fatalf("detail.from = %v, want 2", detail["from"])
	}
	if int(detail["to"].(float64)) != 5 {
		t.Fatalf("detail.to = %v, want 5", detail["to"])
	}
	if detail["reason"].(string) != "session_load" {
		t.Fatalf("detail.reason = %q, want session_load", detail["reason"])
	}
}

func TestController_RecordsAuditEventOnScaleDown(t *testing.T) {
	lister := &fakeLister{apps: []*db.App{app("demo", 3, 1, 8)}}
	signal := &fakeSignal{counts: map[string][]int64{"demo": {1, 1, 1}}} // total 3 -> desired 1
	scaler := newFakeScaler()
	auditor := &fakeAuditor{}
	c := newTestController(lister, signal, scaler, auditor)

	c.reconcile(time.Now())

	if len(auditor.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(auditor.events))
	}
	ev := auditor.events[0]
	if ev.Action != ActionScaleDown {
		t.Fatalf("action = %q, want %q", ev.Action, ActionScaleDown)
	}
}

func TestController_NoAuditEventOnNoOp(t *testing.T) {
	lister := &fakeLister{apps: []*db.App{app("demo", 3, 1, 8)}}
	signal := &fakeSignal{counts: map[string][]int64{"demo": {8, 8, 1}}} // desired=3==current
	scaler := newFakeScaler()
	auditor := &fakeAuditor{}
	c := newTestController(lister, signal, scaler, auditor)

	c.reconcile(time.Now())

	if len(auditor.events) != 0 {
		t.Fatalf("audit events = %d, want 0 for no-op", len(auditor.events))
	}
}

func TestController_NoAuditEventWhenScalePrimitiveRefuses(t *testing.T) {
	// ScaleDown no-op (returns false): no audit event should fire.
	lister := &fakeLister{apps: []*db.App{app("demo", 2, 1, 8)}}
	signal := &fakeSignal{counts: map[string][]int64{"demo": {1, 1}}} // desired 1 < 2
	scaler := newFakeScaler()
	scaler.downNoOp = true
	auditor := &fakeAuditor{}
	c := newTestController(lister, signal, scaler, auditor)

	c.reconcile(time.Now())

	if len(auditor.events) != 0 {
		t.Fatalf("audit events = %d, want 0 when scale primitive refuses", len(auditor.events))
	}
}

type fakeMetrics struct {
	scales map[string]int
}

func (f *fakeMetrics) RecordAutoscaleScale(dir string) {
	if f.scales == nil {
		f.scales = make(map[string]int)
	}
	f.scales[dir]++
}

func TestController_RecordsMetricOnScaleUp(t *testing.T) {
	lister := &fakeLister{apps: []*db.App{app("demo", 2, 1, 8)}}
	signal := &fakeSignal{counts: map[string][]int64{"demo": {20, 20}}} // desired 5 -> scale up
	scaler := newFakeScaler()
	auditor := &fakeAuditor{}
	fm := &fakeMetrics{}
	c := newTestController(lister, signal, scaler, auditor)
	c.SetMetrics(fm)

	c.reconcile(time.Now())

	if fm.scales["up"] != 1 {
		t.Fatalf("RecordAutoscaleScale(up) calls = %d, want 1", fm.scales["up"])
	}
	if fm.scales["down"] != 0 {
		t.Fatalf("unexpected down metric: %d", fm.scales["down"])
	}
}

func TestController_RecordsMetricOnScaleDown(t *testing.T) {
	lister := &fakeLister{apps: []*db.App{app("demo", 3, 1, 8)}}
	signal := &fakeSignal{counts: map[string][]int64{"demo": {1, 1, 1}}} // desired 1 -> scale down
	scaler := newFakeScaler()
	auditor := &fakeAuditor{}
	fm := &fakeMetrics{}
	c := newTestController(lister, signal, scaler, auditor)
	c.SetMetrics(fm)

	c.reconcile(time.Now())

	if fm.scales["down"] != 1 {
		t.Fatalf("RecordAutoscaleScale(down) calls = %d, want 1", fm.scales["down"])
	}
}

func TestController_NoMetricWhenMetricsNotSet(t *testing.T) {
	// SetMetrics never called; must not panic.
	lister := &fakeLister{apps: []*db.App{app("demo", 2, 1, 8)}}
	signal := &fakeSignal{counts: map[string][]int64{"demo": {20, 20}}}
	scaler := newFakeScaler()
	c := newTestController(lister, signal, scaler, &fakeAuditor{})
	// No SetMetrics call.
	c.reconcile(time.Now()) // must not panic
}

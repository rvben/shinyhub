package autoscale

import (
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
}

func newFakeScaler() *fakeScaler {
	return &fakeScaler{ups: map[string]int{}, downs: map[string]int{}}
}
func (f *fakeScaler) ScaleUp(slug string) (bool, error) { f.ups[slug]++; return !f.upNoOp, nil }
func (f *fakeScaler) ScaleDown(slug string, _ time.Duration) (bool, error) {
	f.downs[slug]++
	return !f.downNoOp, nil
}

func testController(lister Lister, signal Signal, scaler Scaler) *Controller {
	return New(Config{
		ScanInterval:  30 * time.Second,
		Cooldown:      3 * time.Minute,
		DrainGrace:    30 * time.Second,
		RejectWindow:  time.Minute,
		DefaultTarget: 0.8, // perReplica = 8 at cap 10
		DefaultCap:    10,
		RuntimeMax:    32,
	}, lister, signal, scaler, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
	c := testController(lister, signal, scaler)

	start := time.Now()
	c.reconcile(start)
	if scaler.downs["demo"] != 1 {
		t.Fatalf("expected a ScaleDown attempt, got %d", scaler.downs["demo"])
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
	c := testController(lister, signal, scaler)

	start := time.Now()
	c.reconcile(start)
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

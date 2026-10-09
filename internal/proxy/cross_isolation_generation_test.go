package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

func crossRequest(p *Proxy, cookies []*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/app/app/", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec
}

func TestCrossIsolationGroupedToMultiplexPreservesReconnectAndRollback(t *testing.T) {
	p := New()
	defer p.Deregister("app")
	p.SetPoolMode("app", config.IsolationGrouped, 4, 4)
	slot := p.reserveWorker("app", "")
	if err := p.RegisterElasticWorker("app", slot, "http://old/", groupedVersionTransport("old"), 10); err != nil {
		t.Fatal(err)
	}
	old := crossRequest(p, nil)
	if got := old.Body.String(); got != "old" {
		t.Fatal(got)
	}
	if _, err := p.StageGenerationWithPolicy("app", 20, 2, GenerationPolicy{Mode: config.IsolationMultiplex}, 0); err != nil {
		t.Fatal(err)
	}
	if mode, _ := p.ServingMode("app"); mode != config.IsolationGrouped {
		t.Fatal("stage changed serving mode")
	}
	for i := 0; i < 2; i++ {
		if err := p.RegisterGenerationReplica("app", 20, i, "http://new/", groupedVersionTransport("new")); err != nil {
			t.Fatal(err)
		}
	}
	if previous, err := p.ActivateGeneration("app", 20); err != nil || previous != 10 {
		t.Fatalf("activate %d %v", previous, err)
	}
	p.SetPoolMode("app", config.IsolationGrouped, 99, 99)
	if mode, _ := p.ServingMode("app"); mode != config.IsolationMultiplex {
		t.Fatal("structural settings mutated drain policy")
	}
	cleared := httptest.NewRecorder()
	clearReq := httptest.NewRequest("GET", "/app/app/", nil)
	p.ClearGenerationAffinity(cleared, clearReq, "app")
	clearCID := false
	for _, c := range cleared.Result().Cookies() {
		if c.Name == clientCookiePrefix+"app" && c.MaxAge < 0 {
			clearCID = true
		}
	}
	if !clearCID {
		t.Fatal("reload does not clear retained grouped affinity")
	}
	if got := crossRequest(p, old.Result().Cookies()).Body.String(); got != "old" {
		t.Fatalf("lost retained binding: %s", got)
	}
	if got := crossRequest(p, nil).Body.String(); got != "new" {
		t.Fatalf("fresh client: %s", got)
	}
	if p.TryRetireGeneration("app", 10) {
		t.Fatal("retired reconnectable generation")
	}
	if p.BeginHibernate("app", time.Now().Add(time.Hour)) {
		t.Fatal("hibernated while draining")
	}
	if p.ElasticSlotCanStart("app", slot) || p.ElasticSlotCanStart("app", 99) {
		t.Fatal("multiplex permits elastic spawn")
	}
	if dep, ok := p.ElasticWorkerGeneration("app", slot); !ok || dep != 10 {
		t.Fatalf("retained ownership %d %v", dep, ok)
	}
	if err := p.RevertGeneration("app", 20, 10); err != nil {
		t.Fatal(err)
	}
	if mode, _ := p.ServingMode("app"); mode != config.IsolationGrouped {
		t.Fatal("rollback policy")
	}
	if got := crossRequest(p, nil).Body.String(); got != "old" {
		t.Fatal(got)
	}
	if !p.RetireGeneration("app", 20) {
		t.Fatal("retire failed target")
	}
	if p.ElasticWorkerCount("app") != 1 {
		t.Fatal("multiplex retirement deleted grouped slot")
	}
}

func TestCrossIsolationMultiplexToGroupedSlotFloorAndPolicy(t *testing.T) {
	p := New()
	defer p.Deregister("app")
	p.SetPoolSize("app", 3)
	for i := 0; i < 3; i++ {
		if err := p.RegisterReplica("app", i, "http://old/", groupedVersionTransport("old"), 10); err != nil {
			t.Fatal(err)
		}
	}
	old := crossRequest(p, nil)
	policy := GenerationPolicy{Mode: config.IsolationGrouped, GroupedSize: 2, MaxWorkers: 3, WarmSpares: 1}
	slot, err := p.StageGenerationWithPolicy("app", 20, 1, policy, 7)
	if err != nil {
		t.Fatal(err)
	}
	if slot < 7 {
		t.Fatalf("reserved below floor: %d", slot)
	}
	if mode, _ := p.ServingMode("app"); mode != config.IsolationMultiplex {
		t.Fatal("staging changed active mode")
	}
	if err := p.RegisterGenerationReplica("app", 20, 0, "http://bad/", groupedVersionTransport("bad")); err == nil {
		t.Fatal("accepted unreserved sparse slot")
	}
	if err := p.RegisterGenerationReplica("app", 20, slot, "http://new/", groupedVersionTransport("new")); err != nil {
		t.Fatal(err)
	}
	if previous, err := p.ActivateGeneration("app", 20); err != nil || previous != 10 {
		t.Fatalf("activate %d %v", previous, err)
	}
	if got := crossRequest(p, old.Result().Cookies()).Body.String(); got != "new" {
		t.Fatal(got)
	}
	next := p.reserveWorker("app", "")
	if next <= slot {
		t.Fatalf("next slot %d reused floor %d", next, slot)
	}
	if !p.ElasticSlotCanStart("app", next) || !p.ElasticSlotCanStart("app", slot) || p.ElasticSlotCanStart("app", 999) {
		t.Fatal("spawn fencing")
	}
	p.mu.Lock()
	p.pools["app"].workers[slot].activeConns.Store(2)
	p.pools["app"].drainingGenerations[10][0].activeConns.Store(1)
	p.mu.Unlock()
	if stat := p.PoolSessionSnapshot()["app"]; stat.Sessions != 3 || stat.Replicas != 1 || stat.Cap != 2 {
		t.Fatalf("accounting %+v", stat)
	}
	if err := p.RevertGeneration("app", 20, 10); err != nil {
		t.Fatal(err)
	}
	if mode, _ := p.ServingMode("app"); mode != config.IsolationMultiplex {
		t.Fatal("rollback policy")
	}
	if got := crossRequest(p, old.Result().Cookies()).Body.String(); got != "old" {
		t.Fatal(got)
	}
}

func TestCrossIsolationTerminateCapturesRemovedWorkerOwnership(t *testing.T) {
	p := New()
	p.SetPoolMode("app", config.IsolationGrouped, 2, 1)
	slot := p.reserveWorker("app", "")
	if err := p.RegisterElasticWorker("app", slot, "http://old/", groupedVersionTransport("old"), 10); err != nil {
		t.Fatal(err)
	}
	calls := make(chan int64, 1)
	p.SetTerminateGenerationFunc(func(slug string, dep int64, index int) {
		if slug != "app" || index != slot {
			calls <- -1
			return
		}
		calls <- dep
	})
	_, err := p.StageGenerationWithPolicy("app", 20, 1, GenerationPolicy{Mode: config.IsolationMultiplex}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.RegisterGenerationReplica("app", 20, 0, "http://new/", groupedVersionTransport("new")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ActivateGeneration("app", 20); err != nil {
		t.Fatal(err)
	}
	p.Deregister("app")
	select {
	case dep := <-calls:
		if dep != 10 {
			t.Fatalf("wrong generation %d", dep)
		}
	case <-time.After(time.Second):
		t.Fatal("lost mixed worker teardown")
	}
}

func TestCrossIsolationIdleGroupedRollback(t *testing.T) {
	p := New()
	defer p.Deregister("app")
	p.SetPoolMode("app", config.IsolationGrouped, 3, 4)
	p.SetPoolWarmSpares("app", 2)
	if err := p.EnsureServingGeneration("app", 10, config.IsolationGrouped); err != nil {
		t.Fatal(err)
	}
	if err := p.EnsureServingGeneration("app", 11, config.IsolationGrouped); err == nil {
		t.Fatal("overwrote serving authority")
	}
	if _, err := p.StageGenerationWithPolicy("app", 20, 1, GenerationPolicy{Mode: config.IsolationMultiplex}, 0); err != nil {
		t.Fatal(err)
	}
	if err := p.RegisterGenerationReplica("app", 20, 0, "http://new/", groupedVersionTransport("new")); err != nil {
		t.Fatal(err)
	}
	if prev, err := p.ActivateGeneration("app", 20); err != nil || prev != 10 {
		t.Fatalf("activate %d %v", prev, err)
	}
	if err := p.RevertGeneration("app", 20, 10); err != nil {
		t.Fatal(err)
	}
	if mode, _ := p.ServingMode("app"); mode != config.IsolationGrouped {
		t.Fatal("failed to restore idle grouped policy")
	}
	p.mu.RLock()
	policy := poolPolicy(p.pools["app"])
	p.mu.RUnlock()
	if policy.GroupedSize != 3 || policy.MaxWorkers != 4 || policy.WarmSpares != 2 {
		t.Fatalf("lost outgoing limits %+v", policy)
	}
}

func TestCrossIsolationBootReservationCannotLaunchAfterCutover(t *testing.T) {
	p := New()
	defer p.Deregister("app")
	p.SetPoolMode("app", config.IsolationGrouped, 2, 3)
	ready := p.reserveWorker("app", "")
	if err := p.RegisterElasticWorker("app", ready, "http://old/", groupedVersionTransport("old"), 10); err != nil {
		t.Fatal(err)
	}
	queued := p.reserveWorker("app", "")
	if !p.ElasticSlotCanStart("app", queued) {
		t.Fatal("active reservation rejected")
	}
	slot, err := p.StageGenerationWithPolicy("app", 20, 1, GenerationPolicy{Mode: config.IsolationGrouped, GroupedSize: 2, MaxWorkers: 3}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.RegisterGenerationReplica("app", 20, slot, "http://new/", groupedVersionTransport("new")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ActivateGeneration("app", 20); err != nil {
		t.Fatal(err)
	}
	if p.ElasticSlotCanStart("app", queued) {
		t.Fatal("old reservation survives cutover spawn fence")
	}
	if err := p.RegisterElasticWorker("app", queued, "http://late/", groupedVersionTransport("late"), 10); err == nil {
		t.Fatal("late old registration accepted")
	}
	p.ReleaseReservation("app", queued)
	if p.ElasticSlotCanStart("app", queued) {
		t.Fatal("missing old reservation permits spawn")
	}
}

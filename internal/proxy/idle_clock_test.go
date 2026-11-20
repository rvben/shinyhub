package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/upgrade"
)

func TestIdleClock_ReadinessWithoutTraffic(t *testing.T) {
	p := New()
	p.SetPoolSize("app", 1)
	before := time.Now()
	if err := p.RegisterReplica("app", 0, "http://localhost:1234", nil, 1); err != nil {
		t.Fatal(err)
	}
	since := p.IdleSince("app")
	if since.Before(before) || since.After(time.Now()) {
		t.Fatalf("idle clock = %v, readiness began at %v", since, before)
	}
	if !p.LastSeen("app").IsZero() {
		t.Fatal("readiness counted as traffic")
	}
	// A cluster sync of an unchanged backend must not postpone hibernation.
	if err := p.RegisterReplica("app", 0, "http://localhost:1234", nil, 1); err != nil {
		t.Fatal(err)
	}
	if !p.IdleSince("app").Equal(since) {
		t.Fatal("unchanged registration reset idle clock")
	}
	if err := p.RegisterReplica("app", 0, "http://localhost:5678", nil, 2); err != nil {
		t.Fatal(err)
	}
	if !p.IdleSince("app").After(since) {
		t.Fatal("new generation did not reset idle clock")
	}
	since = p.IdleSince("app")
	if !p.BeginHibernate("app", since) {
		t.Fatal("never-used pool could not hibernate")
	}
	if !p.IdleSince("app").IsZero() {
		t.Fatal("hibernate retained idle clock")
	}
}

func TestIdleClock_TrafficAndActiveConnections(t *testing.T) {
	p := New()
	p.SetPoolSize("app", 1)
	if err := p.RegisterReplica("app", 0, "http://localhost:1234", nil, 1); err != nil {
		t.Fatal(err)
	}
	snapshot := p.IdleSince("app")
	p.RecordActivity("app")
	if !p.IdleSince("app").After(snapshot) {
		t.Fatal("traffic did not advance idle clock")
	}
	if p.BeginHibernate("app", snapshot) {
		t.Fatal("traffic race did not abort hibernation")
	}
	p.pools["app"].replicas[0].activeConns.Store(1)
	if p.BeginHibernate("app", p.IdleSince("app")) {
		t.Fatal("hibernated with an active connection")
	}
}

func TestIdleClock_RequestCompletionStartsIdleWindow(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	p := New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/app/app/", nil))
	}()
	<-entered
	p.seenMu.Lock()
	started := time.Now().Add(-time.Hour)
	p.lastSeen["app"] = started
	p.seenMu.Unlock()
	if p.BeginHibernate("app", time.Now()) {
		t.Error("hibernated an active request")
	}
	closingAt := time.Now()
	close(release)
	<-done
	if p.IdleSince("app").Before(closingAt) {
		t.Fatal("idle clock did not advance when request closed")
	}
	if !p.LastSeen("app").Equal(started) {
		t.Fatal("request completion counted as new demand for warm expansion")
	}
	if p.BeginHibernate("app", closingAt) {
		t.Fatal("close racing an idle snapshot did not abort hibernation")
	}
	if !p.BeginHibernate("app", p.IdleSince("app")) {
		t.Fatal("completed request prevents eventual hibernation")
	}
}

func TestIdleClock_BackgroundReplacementDoesNotExtendIdle(t *testing.T) {
	for _, elastic := range []bool{false, true} {
		t.Run(fmt.Sprint(elastic), func(t *testing.T) {
			p := New()
			p.SetPoolSize("app", 1)
			if elastic {
				p.SetPoolMode("app", config.IsolationGrouped, 4, 8)
			}
			register := func(slot int, target string) {
				t.Helper()
				var err error
				if elastic {
					err = p.RegisterElasticWorker("app", slot, target, nil, 1)
				} else {
					err = p.RegisterReplica("app", slot, target, nil, 1)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			register(0, "http://localhost:1234")
			old := time.Now().Add(-11 * time.Minute)
			p.pools["app"].idleBaseline = old
			if elastic {
				// Replenish an empty warm-spare pool in the same serving generation.
				p.pools["app"].workers = nil
				register(1, "http://localhost:5678")
			} else {
				register(0, "http://localhost:5678")
			}
			if !p.IdleSince("app").Equal(old) {
				t.Fatal("background replacement reset idle window")
			}
			if !p.BeginHibernate("app", old) {
				t.Fatal("idle replacement cannot hibernate")
			}
		})
	}
}

func TestIdleClock_ReadinessRaceAbortsIdleHibernate(t *testing.T) {
	p := New()
	p.SetPoolSize("app", 1)
	if err := p.RegisterReplica("app", 0, "http://localhost:1234", nil, 1); err != nil {
		t.Fatal(err)
	}
	snapshot := p.IdleSince("app")
	if err := p.RegisterReplica("app", 0, "http://localhost:5678", nil, 2); err != nil {
		t.Fatal(err)
	}
	if p.BeginIdleHibernate("app", snapshot) {
		t.Fatal("idle CAS removed a newly ready generation")
	}
	if !p.BeginIdleHibernate("app", p.IdleSince("app")) {
		t.Fatal("unchanged idle snapshot could not hibernate")
	}
	// Startup restoration intentionally parks a just-booted pool using its
	// pre-boot traffic snapshot, so the explicit parking API must still allow it.
	p.SetPoolSize("app", 1)
	if err := p.RegisterReplica("app", 0, "http://localhost:9999", nil, 3); err != nil {
		t.Fatal(err)
	}
	if !p.BeginHibernate("app", snapshot) {
		t.Fatal("explicit parking rejected post-snapshot readiness")
	}
}

func TestIdleClock_RelayedSessionClosureStartsIdleWindow(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	lease, err := upgrade.NewRelayLease(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	backend := upgrade.RelayBackend{Slug: "app", DeploymentID: 7, Index: 0, TargetURL: "http://localhost:1234", Sessions: 1}
	if err := lease.Publish([]upgrade.RelayBackend{backend}); err != nil {
		t.Fatal(err)
	}
	p := New()
	p.SetRelayLeases(&upgrade.RelayLeases{Dir: dir})
	p.SetPoolSize("app", 1)
	if err := p.RegisterReplica("app", 0, backend.TargetURL, nil, 7); err != nil {
		t.Fatal(err)
	}
	p.pools["app"].idleBaseline = time.Now().Add(-time.Hour)
	activeSnapshot := p.IdleSince("app")
	if p.BeginIdleHibernate("app", activeSnapshot) {
		t.Fatal("hibernated an active relayed session")
	}
	if err := lease.Publish(nil); err != nil {
		t.Fatal(err)
	}
	closedAt := time.Now()
	since := p.IdleSince("app")
	if since.Before(closedAt) {
		t.Fatal("released relay did not begin a fresh idle window")
	}
	if !p.LastSeen("app").IsZero() {
		t.Fatal("relay observation counted as new traffic")
	}
	if !p.IdleSince("app").Equal(since) {
		t.Fatal("idle relay observation indefinitely postponed hibernation")
	}
	if p.BeginIdleHibernate("app", activeSnapshot) {
		t.Fatal("relay closure race did not abort idle CAS")
	}
	if !p.BeginIdleHibernate("app", since) {
		t.Fatal("closed relay prevented eventual hibernation")
	}
}

// A relayed connection closes in the retiring process, so this proxy records
// no close for it. The quiet window a stop-first deploy honors must start when
// the lease view sees the connection end, with nothing having watched it open.
func TestDeregisterIfIdle_RelayedSessionClosureStartsQuietWindow(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	lease, err := upgrade.NewRelayLease(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	backend := upgrade.RelayBackend{Slug: "app", DeploymentID: 7, Index: 0, TargetURL: "http://localhost:1234", Sessions: 1}
	if err := lease.Publish([]upgrade.RelayBackend{backend}); err != nil {
		t.Fatal(err)
	}
	leases := &upgrade.RelayLeases{Dir: dir}
	leases.Refresh()
	p := New()
	p.SetRelayLeases(leases)
	p.SetPoolSize("app", 1)
	if err := p.RegisterReplica("app", 0, backend.TargetURL, nil, 7); err != nil {
		t.Fatal(err)
	}
	p.SetPoolSize("other", 1)
	if err := p.RegisterReplica("other", 0, "http://localhost:1235", nil, 8); err != nil {
		t.Fatal(err)
	}
	if err := lease.Publish(nil); err != nil {
		t.Fatal(err)
	}
	leases.Refresh()
	want := "the previous server process released its last connection in the last 1h0m0s"
	if reason := p.DeregisterIfIdle("app", time.Hour); reason != want {
		t.Fatalf("deregister right after the relayed session closed = %q, want %q", reason, want)
	}
	if !p.PoolHasAny("app") {
		t.Fatal("refused deregister still dropped the route")
	}
	if reason := p.DeregisterIfIdle("other", time.Hour); reason != "" {
		t.Fatalf("an app that had no relayed session was held back: %s", reason)
	}
	leases.Refresh()
	if reason := p.DeregisterIfIdle("app", time.Hour); reason != want {
		t.Fatalf("a later refresh forgot the release: %q", reason)
	}
	if reason := p.DeregisterIfIdle("app", time.Nanosecond); reason != "" {
		t.Fatalf("pool was not removed once the quiet window had passed: %s", reason)
	}
}

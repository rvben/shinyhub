package api

import (
	"testing"

	"github.com/rvben/shinyhub/internal/appstatus"
	"github.com/rvben/shinyhub/internal/db"
)

// The elastic branch of decorateAppObservation: an elastic pool has no durable
// replica rows, so status is derived from the proxy's live worker view. Every
// row here is a shape a grouped / per_session app takes in production.
func TestDecorateAppObservation_ElasticPool(t *testing.T) {
	cases := []struct {
		name       string
		desired    string
		workers    []string // proxy worker status labels
		wantStatus string
		wantWorker int // workers_running
	}{
		{"desired running, empty pool", "running", nil, appstatus.Idle, 0},
		{"desired degraded, empty pool", "degraded", nil, appstatus.Idle, 0},
		{"one worker booting", "running", []string{"booting"}, appstatus.Starting, 0},
		{"one worker resuming", "running", []string{"resuming"}, appstatus.Starting, 0},
		{"one worker running", "running", []string{"running"}, appstatus.Running, 1},
		{"running beside booting", "running", []string{"running", "booting"}, appstatus.Running, 1},
		// A frozen warm spare is ready, not running: it resumes on the first
		// request. The pool is idle, never stuck in starting.
		{"only a frozen warm spare", "running", []string{"suspended"}, appstatus.Idle, 0},
		// A spare still being frozen is unassignable, like a booting worker:
		// the allocator rejects a pool whose only slot is freezing, so the
		// app must not read idle (ready) until the freeze completes.
		{"spare freezing", "running", []string{"suspending"}, appstatus.Starting, 0},
		{"spare and a running worker", "running", []string{"suspended", "running"}, appstatus.Running, 1},
		{"spare and a booting worker", "running", []string{"suspended", "booting"}, appstatus.Starting, 0},
		// A draining worker is on its way out; it neither serves nor boots.
		{"only a draining worker", "running", []string{"draining"}, appstatus.Idle, 0},
		// The metrics path substitutes the manager's health view for a
		// tracked worker; a crashed process is not a running worker.
		{"manager reports crashed", "running", []string{"crashed"}, appstatus.Idle, 0},
		// Intent other than running is reported as-is when nothing serves.
		{"desired stopped, empty pool", "stopped", nil, appstatus.Stopped, 0},
		{"desired hibernated, empty pool", "hibernated", nil, appstatus.Hibernated, 0},
	}
	s := &Server{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &db.App{Slug: "elastic-app", Status: tc.desired, DesiredStatus: tc.desired}
			pool := elasticPool{Known: true}
			for _, w := range tc.workers {
				pool.observe(w)
			}
			s.decorateAppObservation(app, nil, pool)
			if app.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", app.Status, tc.wantStatus)
			}
			if app.WorkersRunning != tc.wantWorker {
				t.Errorf("workers_running = %d, want %d", app.WorkersRunning, tc.wantWorker)
			}
			if app.DesiredStatus != tc.desired {
				t.Errorf("desired_status = %q, want %q (must stay the stored intent)", app.DesiredStatus, tc.desired)
			}
			if app.ReplicasRunning != 0 {
				t.Errorf("replicas_running = %d, want 0 for an elastic pool", app.ReplicasRunning)
			}
		})
	}
}

// Every status the elastic branch emits must be one the shared vocabulary
// knows, otherwise a CLI gate reading it would wait on it forever.
func TestDecorateAppObservation_ElasticEmitsKnownStatuses(t *testing.T) {
	s := &Server{}
	for _, desired := range []string{"running", "degraded", "stopped", "hibernated", "deleting"} {
		for _, workers := range [][]string{nil, {"booting"}, {"running"}, {"suspended"}, {"draining"}} {
			app := &db.App{Slug: "elastic-app", Status: desired, DesiredStatus: desired}
			pool := elasticPool{Known: true}
			for _, w := range workers {
				pool.observe(w)
			}
			s.decorateAppObservation(app, nil, pool)
			if appstatus.Class(app.Status) == appstatus.KindUnknown {
				t.Errorf("desired=%q workers=%v: emitted status %q is not in appstatus.Observed", desired, workers, app.Status)
			}
		}
	}
}

// A multiplex app is unaffected by the elastic branch: with Known=false the
// replica rows decide, and a desired-running app with no replicas is degraded
// (a real gap), not idle.
func TestDecorateAppObservation_MultiplexIgnoresPool(t *testing.T) {
	s := &Server{}
	app := &db.App{Slug: "mux", Status: "running", DesiredStatus: "running"}
	s.decorateAppObservation(app, nil, elasticPool{})
	if app.Status != appstatus.Degraded {
		t.Fatalf("multiplex desired-running with no replicas: status = %q, want degraded", app.Status)
	}
	app = &db.App{Slug: "mux", Status: "running", DesiredStatus: "running"}
	s.decorateAppObservation(app, []*db.Replica{{Index: 0, Status: db.ReplicaStatusRunning}}, elasticPool{})
	if app.Status != appstatus.Running || app.ReplicasRunning != 1 {
		t.Fatalf("multiplex with one running replica: status=%q replicas_running=%d", app.Status, app.ReplicasRunning)
	}
}

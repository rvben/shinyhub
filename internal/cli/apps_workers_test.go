package cli

import (
	"strings"
	"testing"
)

// TestAppsShow_RendersWorkerPool verifies the per-worker capacity view for
// elastic apps: an Isolation summary, the elastic admission ceiling, and a
// Workers table with slot, routing status, bound sessions vs cap, pid, port.
// The multiplex Replicas/Max sess-r/ceiling lines are suppressed - their
// arithmetic does not apply to elastic pools and would mislead.
func TestAppsShow_RendersWorkerPool(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	// replicas_status carries a stale row from a pre-isolation-switch deploy;
	// for an elastic app the Workers table is the truth and the multiplex
	// Replicas section must not render next to it.
	setResp(200, `{"app":{"slug":"demo","name":"Demo","owner_id":7,"access":"private","status":"running","replicas":1,"max_sessions_per_replica":10,"deploy_count":3,"created_at":"2026-07-09T10:00:00Z","worker_isolation":"grouped"},"effective_max_sessions_per_replica":10,"replicas_status":[{"index":0,"status":"running","pid":4321,"port":20101}],"worker_pool":{"mode":"grouped","sessions_per_worker":2,"max_workers":5,"ceiling":10,"workers":[{"slot_id":0,"status":"running","sessions":2,"pid":4321,"port":20101},{"slot_id":1,"status":"booting","sessions":1}]}}`)

	out, err := execCLI(t, "apps", "show", "demo", "-o", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		"Isolation:   grouped (2 sessions/worker, max 5 workers)",
		"Admission ceiling: 5 × 2 = 10 concurrent sessions",
		"Workers:",
		"SLOT   STATUS     SESSIONS  PID      PORT",
		"0      running    2/2       4321     20101",
		"1      booting    1/2       -        -",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	for _, reject := range []string{"Max sess/r:", "Replicas:    1", "INDEX  STATUS"} {
		if strings.Contains(out, reject) {
			t.Errorf("elastic app must not render the multiplex line %q:\n%s", reject, out)
		}
	}
}

// TestAppsShow_WorkerPoolEmpty pins the zero-worker state: an elastic app
// with a live pool but no workers yet says so instead of hiding the section
// (an empty pool is a real state, distinct from "no capacity view").
func TestAppsShow_WorkerPoolEmpty(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"app":{"slug":"demo","name":"Demo","owner_id":7,"access":"private","status":"running","replicas":1,"deploy_count":1,"created_at":"2026-07-09T10:00:00Z","worker_isolation":"per_session"},"worker_pool":{"mode":"per_session","sessions_per_worker":1,"max_workers":4,"ceiling":4,"workers":[]}}`)

	out, err := execCLI(t, "apps", "show", "demo", "-o", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		"Isolation:   per_session (1 session/worker, max 4 workers)",
		"Workers:     none yet (spawned on demand)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestAppsMetrics_ElasticSummary verifies the metrics table summarizes the
// elastic pool (mode + ceiling arithmetic) and renders booting slots that
// have no process yet.
func TestAppsMetrics_ElasticSummary(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"status":"running","sessions_cap":2,"worker_isolation":"grouped","max_workers":5,"metrics_available":true,"autoscale_status":null,"replicas":[{"index":0,"status":"running","pid":4321,"cpu_percent":1.5,"rss_bytes":1048576,"sessions":2,"tier":"local","provider":"native","metrics_available":true},{"index":1,"status":"booting","sessions":1,"metrics_available":false}]}`)

	out, err := execCLI(t, "apps", "metrics", "demo", "-o", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		"grouped · ceiling 10 (max 5 workers × 2/worker)",
		"booting",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sessions cap 2") {
		t.Errorf("elastic app must render the ceiling summary, not the multiplex 'sessions cap' line:\n%s", out)
	}
}

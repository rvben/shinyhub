package cli

import (
	"net/http"
	"strings"
	"testing"
)

const scheduleStatusBody = `[
  {"slug":"jp-dash","schedule":"refresh-data","enabled":true,"last_run_at":"2026-06-30T06:00:00Z","last_run_status":"succeeded","last_success_at":"2026-06-30T06:00:00Z","last_success_age_s":7200,"stale":false},
  {"slug":"ccro-kpi","schedule":"refresh-data","enabled":true,"last_run_at":"2026-06-30T06:00:00Z","last_run_status":"failed","last_success_at":null,"last_success_age_s":null,"stale":true}
]`

func TestScheduleStatus_Table(t *testing.T) {
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/fleet/schedules/status") {
			_, _ = w.Write([]byte(scheduleStatusBody))
			return
		}
		w.WriteHeader(404)
	})
	out, err := execCLI(t, "schedule", "status", "-o", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	// never (never-succeeded) is distinct from a stale-but-once-succeeded row.
	for _, want := range []string{"APP", "SCHEDULE", "STALE", "never", "yes"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q:\n%s", want, out)
		}
	}
}

func TestScheduleStatus_SlugFilter(t *testing.T) {
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/fleet/schedules/status") {
			_, _ = w.Write([]byte(scheduleStatusBody))
			return
		}
		w.WriteHeader(404)
	})
	if _, err := execCLI(t, "schedule", "status", "jp-dash", "-o", "table"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	if !strings.Contains((*reqs)[0].Query, "slug=jp-dash") {
		t.Errorf("a slug arg should filter via ?slug=, got query %q", (*reqs)[0].Query)
	}
}

func TestScheduleStatus_JSONEnvelope(t *testing.T) {
	_, _ = setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/fleet/schedules/status") {
			_, _ = w.Write([]byte(scheduleStatusBody))
			return
		}
		w.WriteHeader(404)
	})
	out, err := execCLI(t, "schedule", "status") // piped => renderList JSON envelope
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// renderList wraps rows in an {items,total,...} envelope; assert the shape
	// so a regression that forwards the raw server array is caught.
	if !strings.Contains(out, `"items"`) {
		t.Fatalf("json output should be a renderList envelope with an items key:\n%s", out)
	}
	if !strings.Contains(out, `"slug":"jp-dash"`) || !strings.Contains(out, `"stale":true`) {
		t.Fatalf("json output missing rows:\n%s", out)
	}
}

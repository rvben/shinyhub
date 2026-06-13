package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// scheduleRunsHandler routes the two-request flow: GET .../schedules resolves
// the name to id 1, GET .../schedules/1/runs returns the run history.
func scheduleRunsHandler(runsJSON string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/schedules"):
			_, _ = w.Write([]byte(`[{"id":1,"name":"daily-fetch"}]`))
		case strings.HasSuffix(r.URL.Path, "/schedules/1/runs"):
			_, _ = w.Write([]byte(runsJSON))
		default:
			w.WriteHeader(404)
		}
	}
}

const runsBody = `[
  {"id":4,"schedule_id":1,"status":"succeeded","trigger":"manual","started_at":"2026-06-13T10:22:13.117414Z","finished_at":"2026-06-13T10:22:13.281299Z","exit_code":0},
  {"id":3,"schedule_id":1,"status":"failed","trigger":"register","started_at":"2026-06-13T10:21:40.500000Z","finished_at":"2026-06-13T10:21:41.700000Z","exit_code":1}
]`

func TestScheduleRuns_Table(t *testing.T) {
	_, reqs := setupCLITestHandler(t, scheduleRunsHandler(runsBody))

	out, err := execCLI(t, "schedule", "runs", "ed-fetcher", "daily-fetch", "-o", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*reqs) != 2 {
		t.Fatalf("expected resolve + runs (2 requests), got %d: %+v", len(*reqs), *reqs)
	}
	if (*reqs)[1].Path != "/api/apps/ed-fetcher/schedules/1/runs" {
		t.Errorf("second request should fetch runs by id, got %s", (*reqs)[1].Path)
	}
	for _, want := range []string{"STATUS", "TRIGGER", "DURATION", "succeeded", "failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q:\n%s", want, out)
		}
	}
	// 281299 - 117414 microseconds ~= 164ms; assert a computed duration appears.
	if !strings.Contains(out, "164ms") {
		t.Errorf("expected computed duration ~164ms:\n%s", out)
	}
}

func TestScheduleRuns_JSONEnvelope(t *testing.T) {
	_, _ = setupCLITestHandler(t, scheduleRunsHandler(runsBody))

	out, err := execCLI(t, "schedule", "runs", "ed-fetcher", "daily-fetch") // piped => JSON
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var env map[string]any
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); jerr != nil {
		t.Fatalf("not valid JSON: %v\n%q", jerr, out)
	}
	if env["total"] != float64(2) {
		t.Errorf("total = %v, want 2", env["total"])
	}
}

func TestScheduleRuns_UnknownScheduleErrors(t *testing.T) {
	_, _ = setupCLITestHandler(t, scheduleRunsHandler(runsBody))

	_, err := execCLI(t, "schedule", "runs", "ed-fetcher", "no-such-job")
	if err == nil {
		t.Fatal("expected an error for an unknown schedule name")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should explain the schedule was not found: %v", err)
	}
}

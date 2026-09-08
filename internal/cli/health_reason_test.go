package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthWaitReasonUsesCurrentServerEvidence(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"schedule run", `{"app":{"status":"degraded"},"deploy_trigger_schedules":[{"name":"refresh-data","satisfied":false,"convergence_status":"running","convergence_run_id":449}]}`, "Waiting for refresh-data run #449"},
		{"schedule failed", `{"deploy_trigger_schedules":[{"name":"refresh-data","satisfied":false,"convergence_status":"failed","convergence_error":"producer exited with code 1"}]}`, "Schedule refresh-data: producer exited with code 1"},
		{"repair", `{"producer_repair_required":true}`, "Producer data needs repair before consumers can start"},
		{"quarantine", `{"compatibility_quarantined":true}`, "Consumer starts blocked: data compatibility is unverified"},
		{"activation", `{"latest_schedule_activations":[{"schedule_name":"refresh-data","schedule_run_id":449,"status":"deferred_capacity","defer_reason":"waiting for spare capacity"}]}`, "Activating data from refresh-data run #449: waiting for spare capacity"},
		{"activation retry", `{"latest_schedule_activations":[{"schedule_name":"refresh-data","status":"running","phase":"starting_replicas","last_error":"old failure"}]}`, "Activating data from refresh-data: starting replicas"},
		{"repair producer", `{"producer_repair_required":true,"deploy_trigger_schedules":[{"name":"refresh-data","satisfied":false,"convergence_status":"running","convergence_run_id":449}]}`, "Waiting for refresh-data run #449; Producer data needs repair before consumers can start"},
		{"replica", `{"app":{"status":"degraded"},"replicas_status":[{"index":2,"status":"lost","reason":"worker unavailable"}]}`, "Replica 2: worker unavailable"},
		{"reported probe error", `{"app":{"status":"starting","last_replica_error":"Health check failed: HTTP 503"}}`, "Health check failed: HTTP 503"},
		{"current version", `{"app":{"status":"running"},"redeploy_in_flight":true}`, "Deployment in progress; current version is serving"},
		{"stale exit ignored", `{"app":{"status":"running"},"replicas_status":[{"index":0,"status":"running","last_exit":{"exit_reason":"old process crashed"}}]}`, ""},
		{"stale activation ignored", `{"latest_schedule_activations":[{"schedule_name":"refresh-data","status":"failed","last_error":"old failure"}]}`, ""},
		{"satisfied schedule ignored", `{"deploy_trigger_schedules":[{"name":"refresh-data","satisfied":true,"convergence_error":"old failure"}]}`, ""},
		{"older server", `{"app":{"status":"starting"}}`, ""},
		{"absent gate flag", `{"deploy_trigger_schedules":[{"name":"refresh-data"}]}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var observation appHealthObservation
			if err := json.Unmarshal([]byte(tc.body), &observation); err != nil {
				t.Fatal(err)
			}
			if got := healthWaitReason(observation); got != tc.want {
				t.Fatalf("reason=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestFleetHealthReasonChangesAreImmediateAndNotStale(t *testing.T) {
	var out bytes.Buffer
	now := time.Unix(0, 0)
	reasons := []string{"Waiting for refresh-data run #449", "Replica 0 is starting", ""}
	calls := 0
	err := waitForFleetHealthLoop("demo", 3*time.Second, time.Second, time.Minute, func() (bool, string, error) {
		calls++
		return false, "degraded", nil
	}, func() time.Time { return now }, func(delay time.Duration) { now = now.Add(delay) }, &out, func() string {
		if calls > len(reasons) {
			return ""
		}
		return reasons[calls-1]
	})
	if err == nil {
		t.Fatal("expected timeout")
	}
	for _, want := range []string{"degraded; Waiting for refresh-data run #449", "degraded; Replica 0 is starting", "demo: degraded (2s elapsed"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing transition %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(err.Error(), "run #449") || strings.Contains(err.Error(), "Replica 0") {
		t.Fatalf("timeout used old reason: %v", err)
	}
}

func TestFleetHealthFailureIncludesObservedReasonAndCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/apps/demo" {
			io.WriteString(w, `{"app":{"status":"crashed","last_replica_error":"Health check failed: HTTP 503"}}`)
			return
		}
		io.WriteString(w, "")
	}))
	defer srv.Close()
	err := waitForFleetHealthy(&cliConfig{Host: srv.URL}, "demo", io.Discard, time.Second)
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") || !strings.Contains(err.Error(), "shinyhub apps logs demo --system --tail 200") {
		t.Fatalf("failure=%v", err)
	}
}

func TestFleetHealthLiveReasonReplacesBareStatus(t *testing.T) {
	d := testFleetDisplay(io.Discard, "demo")
	out, _ := beginFleetApp(d, "demo")
	now := time.Unix(0, 0)
	_ = waitForFleetHealthLoop("demo", time.Second, time.Second, time.Minute, func() (bool, string, error) { return false, "degraded", nil }, func() time.Time { return now }, func(delay time.Duration) { now = now.Add(delay) }, out, func() string { return "Waiting for refresh-data run #449" })
	if d.rows[0].detail != "Waiting for refresh-data run #449" {
		t.Fatalf("row=%+v", d.rows[0])
	}
}

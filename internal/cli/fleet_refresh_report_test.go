package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/fleet"
)

func TestFleetRefreshReportSeparatesRecoveryFromDeployment(t *testing.T) {
	r := applyResult{
		slug: "dashboard", action: fleet.ActionUnchanged, status: statusUnchanged,
		mutation:          mutationCommitted,
		scheduleRefreshes: []scheduleRefreshOutcome{{Schedule: "refresh-data", RunID: 42, Disposition: "started", Status: "succeeded"}},
	}
	var text bytes.Buffer
	if err := renderApplyReport(&text, "dashboards", applyOutcome{apps: []applyResult{r}}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "refresh-data: refresh started run #42 (succeeded)") {
		t.Fatalf("refresh identity absent: %s", text.String())
	}
	var encoded bytes.Buffer
	if err := writeFleetApplyJSON(&encoded, &fleet.Manifest{FleetID: "dashboards"}, "http://h", []fleet.AppDiff{{Slug: r.slug, Action: r.action}}, nil, applyOutcome{apps: []applyResult{r}}, 0, "OK"); err != nil {
		t.Fatal(err)
	}
	var envelope applyJSONEnvelope
	if err := json.Unmarshal(encoded.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	app := envelope.Apps[0]
	if len(app.DeployRuns) != 0 || len(app.ScheduleRefreshes) != 1 || app.ScheduleRefreshes[0].RunID != 42 {
		t.Fatalf("refresh must have its own outcome: %+v", app)
	}
	if committed, partial, unknown := applyMutationCounts([]applyResult{r}); committed != 1 || partial != 0 || unknown != 0 {
		t.Fatalf("successful refresh mutation counts = %d/%d/%d", committed, partial, unknown)
	}
}

func TestObservedRefreshWorkRequiresSuccessfulRun(t *testing.T) {
	for _, tc := range []struct {
		status string
		runID  int64
		want   bool
	}{{"succeeded", 42, true}, {"failed", 42, false}, {"running", 42, false}, {"", 0, false}} {
		r := applyResult{scheduleRefreshes: []scheduleRefreshOutcome{{RunID: tc.runID, Status: tc.status}}}
		if got := observedScheduleConvergenceWork(r); got != tc.want {
			t.Errorf("run %d status %q: observed = %t, want %t", tc.runID, tc.status, got, tc.want)
		}
	}
}

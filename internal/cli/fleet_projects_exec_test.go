package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/fleet"
)

// fleetProjectSrv is the house pattern from fleet_apply_exec_test.go:24-29 (an
// inline httptest server plus a cliConfig pointed at it), factored out because
// this file needs it six times. There is no package-wide fake-server helper;
// do not add one.
func fleetProjectSrv(t *testing.T, h http.HandlerFunc) *cliConfig {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &cliConfig{Host: srv.URL, Token: "shk_test"}
}

// jsonBody decodes a request body into a map. The package has no decodeBody.
func jsonBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return m
}

func TestConvergeProjectsCreateCarriesMetadata(t *testing.T) {
	var posted map[string]any
	cfg := fleetProjectSrv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/projects" {
			posted = jsonBody(t, r)
			w.WriteHeader(http.StatusCreated)
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	})
	pf := &preflightResult{
		manifest: &fleet.Manifest{FleetID: "acme", Projects: []fleet.ProjectEntry{
			{Slug: "p", Name: strp("P"), Icon: strp("📊")},
		}},
		projectDiff: []fleet.ProjectDiff{{Slug: "p", Action: fleet.ActionCreate, Drift: []fleet.ConfigDriftItem{
			{Key: "name"}, {Key: "icon"},
		}}},
	}
	res := convergeProjects(cfg, pf, convergeOpts{}, &bytes.Buffer{})
	if len(res) != 1 || res[0].status != statusCreated {
		t.Fatalf("res = %+v, want one created", res)
	}
	// Created already named: a slug-only POST followed by a PATCH would
	// reintroduce the unnamed window the project-first ordering exists to close.
	if posted["name"] != "P" || posted["icon_emoji"] != "📊" {
		t.Errorf("POST body = %v, want the declared metadata", posted)
	}
}

func TestConvergeProjectsCreateFallsBackToPatchOn200(t *testing.T) {
	var calls []string
	cfg := fleetProjectSrv(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method)
		if r.Method == http.MethodPost {
			// Created between the plan and the apply, e.g. by a concurrent deploy
			// of another app in the same project. The idempotent POST deliberately
			// does not overwrite, so the metadata still has to be PATCHed.
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	pf := &preflightResult{
		manifest: &fleet.Manifest{FleetID: "acme", Projects: []fleet.ProjectEntry{{Slug: "p", Name: strp("P")}}},
		projectDiff: []fleet.ProjectDiff{{Slug: "p", Action: fleet.ActionCreate,
			Drift: []fleet.ConfigDriftItem{{Key: "name"}}}},
	}
	res := convergeProjects(cfg, pf, convergeOpts{}, &bytes.Buffer{})
	if res[0].status != statusCreated {
		t.Errorf("status = %v, want created", res[0].status)
	}
	if len(calls) != 2 || calls[0] != http.MethodPost || calls[1] != http.MethodPatch {
		t.Errorf("calls = %v, want POST then PATCH", calls)
	}
}

func TestConvergeProjectsUpdateSendsOnlyDriftedKeys(t *testing.T) {
	var patched map[string]any
	cfg := fleetProjectSrv(t, func(w http.ResponseWriter, r *http.Request) {
		patched = jsonBody(t, r)
		w.WriteHeader(http.StatusOK)
	})
	pf := &preflightResult{
		manifest: &fleet.Manifest{FleetID: "acme", Projects: []fleet.ProjectEntry{
			{Slug: "p", Name: strp("P"), Description: strp("D"), Icon: strp("📊")},
		}},
		projectDiff: []fleet.ProjectDiff{{Slug: "p", Action: fleet.ActionUpdateConfig,
			Drift: []fleet.ConfigDriftItem{{Key: "name"}}}},
	}
	res := convergeProjects(cfg, pf, convergeOpts{}, &bytes.Buffer{})
	if res[0].status != statusUpdated {
		t.Fatalf("status = %v, want updated", res[0].status)
	}
	if len(patched) != 1 {
		t.Errorf("PATCH body = %v, want only the drifted name", patched)
	}
}

func TestConvergeProjectsUnchangedMakesNoCall(t *testing.T) {
	cfg := fleetProjectSrv(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unchanged project must make no call, got %s %s", r.Method, r.URL.Path)
	})
	pf := &preflightResult{
		manifest:    &fleet.Manifest{FleetID: "acme", Projects: []fleet.ProjectEntry{{Slug: "p"}}},
		projectDiff: []fleet.ProjectDiff{{Slug: "p", Action: fleet.ActionUnchanged}},
	}
	if res := convergeProjects(cfg, pf, convergeOpts{}, &bytes.Buffer{}); res[0].status != statusUnchanged {
		t.Errorf("status = %v, want unchanged", res[0].status)
	}
}

func TestConvergeProjectsFailureIsPartialNotFatal(t *testing.T) {
	cfg := fleetProjectSrv(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	pf := &preflightResult{
		manifest:    &fleet.Manifest{FleetID: "acme", Projects: []fleet.ProjectEntry{{Slug: "p", Name: strp("P")}}},
		projectDiff: []fleet.ProjectDiff{{Slug: "p", Action: fleet.ActionUpdateConfig, Drift: []fleet.ConfigDriftItem{{Key: "name"}}}},
	}
	res := convergeProjects(cfg, pf, convergeOpts{}, &bytes.Buffer{})
	if res[0].status != statusFailed || res[0].err == nil {
		t.Fatalf("res = %+v, want a failed result carrying the error", res[0])
	}
	// A failed project must raise the run's exit code, or a fleet apply that
	// could not name a project would report OK.
	code, reason := applyExitCode(applyOutcome{projects: res}.all())
	if code != 4 {
		t.Errorf("exit code = %d, want 4", code)
	}
	// The reason must not claim an "app" failed: a project-only failure that
	// prints "1 app(s) failed after retries" misreports the cause to the
	// operator, since no app was ever touched by this run.
	if strings.Contains(reason, "app(s)") {
		t.Errorf("reason = %q, must not name apps for a project-only failure", reason)
	}
}

// EXIT-P: a project-only drift must reach --detailed-exitcode. Without the
// projects parameter on pending(), a fleet whose only change is a project
// rename exits 0 and a CI gate reports the fleet converged.
func TestPendingCountsProjectDrift(t *testing.T) {
	apps := []fleet.AppDiff{{Slug: "a", Action: fleet.ActionUnchanged}}
	if pending(apps, nil) {
		t.Fatal("apps-only unchanged must not be pending (control)")
	}
	if !pending(apps, []fleet.ProjectDiff{{Slug: "p", Action: fleet.ActionUpdateConfig}}) {
		t.Error("a project-only rename must count as pending")
	}
	if pending(apps, []fleet.ProjectDiff{{Slug: "p", Action: fleet.ActionUnchanged}}) {
		t.Error("a converged project must not report pending on the second run")
	}
}

// The summary tally and the exit code must agree: a created project counts in
// both, or `fleet plan` prints "1 to create" and exits 0.
func TestSharedPlanCountsIncludeProjects(t *testing.T) {
	projects := []fleet.ProjectDiff{
		{Slug: "a", Action: fleet.ActionCreate},
		{Slug: "b", Action: fleet.ActionUpdateConfig},
		{Slug: "c", Action: fleet.ActionUnchanged},
	}
	resources := make([]planResource, 0, len(projects))
	for _, project := range projects {
		resources = append(resources, fleetProjectPlanResource(project))
	}
	c := countsFromPlanResources(resources)
	if c.Create != 1 || c.Update != 1 || c.Unchanged != 1 {
		t.Errorf("counts = %+v, want 1/1/1", c)
	}
}

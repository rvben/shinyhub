package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/fleet"
)

func TestNewRunID_UniqueAndShaped(t *testing.T) {
	a, b := newRunID(), newRunID()
	if a == b {
		t.Fatal("run ids must be unique")
	}
	if len(a) != 32 {
		t.Fatalf("run id len = %d, want 32 hex chars", len(a))
	}
}

func TestDecorateFleetRequest_SetsRunAndUA(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://x/api/apps", nil)
	decorateFleetRequest(req, "run123")
	if req.Header.Get("X-Shinyhub-Run-Id") != "run123" {
		t.Fatalf("run id header = %q", req.Header.Get("X-Shinyhub-Run-Id"))
	}
	if ua := req.Header.Get("User-Agent"); !strings.HasPrefix(ua, "shinyhub-fleet/") {
		t.Fatalf("user-agent = %q", ua)
	}
}

func TestSetPrecondition_DigestAndManagedBy(t *testing.T) {
	// digest only
	r1, _ := http.NewRequest("PATCH", "http://x", nil)
	dg := "sha256:abc"
	setPrecondition(r1, &dg, nil)
	if r1.Header.Get("X-Shinyhub-If-Content-Digest") != "sha256:abc" {
		t.Fatalf("digest precondition = %q", r1.Header.Get("X-Shinyhub-If-Content-Digest"))
	}
	if _, ok := r1.Header["X-Shinyhub-If-Managed-By"]; ok {
		t.Fatal("managed-by header must be absent when nil")
	}
	// managed-by present-but-empty asserts "currently unmanaged"
	r2, _ := http.NewRequest("PATCH", "http://x", nil)
	empty := ""
	setPrecondition(r2, nil, &empty)
	if _, ok := r2.Header["X-Shinyhub-If-Managed-By"]; !ok {
		t.Fatal("managed-by header must be present (even empty) to assert unmanaged")
	}
	if r2.Header.Get("X-Shinyhub-If-Managed-By") != "" {
		t.Fatalf("managed-by = %q, want empty", r2.Header.Get("X-Shinyhub-If-Managed-By"))
	}
}

func TestIsConflict(t *testing.T) {
	if !isConflict(&http.Response{StatusCode: 409}) {
		t.Fatal("409 must be a conflict")
	}
	if isConflict(&http.Response{StatusCode: 200}) {
		t.Fatal("200 is not a conflict")
	}
}

func TestBuildFleetProvenance_GitLabAutoDetection(t *testing.T) {
	env := map[string]string{
		"GITLAB_CI": "true", "CI_PIPELINE_IID": "412", "CI_PIPELINE_URL": "https://gitlab.example/pipelines/412",
		"CI_JOB_NAME": "deploy-production", "CI_JOB_URL": "https://gitlab.example/jobs/99",
		"CI_PROJECT_URL": "https://gitlab.example/acme/apps", "CI_COMMIT_SHA": "abcdef1234567890", "CI_COMMIT_REF_NAME": "main",
		"CI_MERGE_REQUEST_IID": "87",
	}
	m, err := buildFleetProvenance(&fleetApplyFlags{provenanceMode: "auto"}, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if m.Provider != "gitlab" || m.Source == nil || m.Source.Label != "GitLab pipeline #412" {
		t.Fatalf("source=%#v", m)
	}
	if m.Revision == nil || m.Revision.URL != "https://gitlab.example/acme/apps/-/commit/abcdef1234567890" {
		t.Fatalf("revision=%#v", m.Revision)
	}
	if m.Change == nil || m.Change.URL != "https://gitlab.example/acme/apps/-/merge_requests/87" {
		t.Fatalf("change=%#v", m.Change)
	}
}

func TestBuildFleetProvenance_NoneRejectsExplicitFields(t *testing.T) {
	_, err := buildFleetProvenance(&fleetApplyFlags{provenanceMode: "none", sourceURL: "https://example.test/p"}, func(string) string { return "" })
	if err == nil {
		t.Fatal("expected conflict between none and explicit source")
	}
}

func TestRecordAppFleetStateSendsNormalizedDeclarationAndRun(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/apps/demo/fleet-state" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Shinyhub-Run-Id") != "run-42" {
			t.Fatalf("run header = %q", r.Header.Get("X-Shinyhub-Run-Id"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	stateChanged := true
	err := recordAppFleetState(&cliConfig{Host: srv.URL, Token: "token"}, "demo",
		fleetConvergenceInSync, "sha256:expected",
		[]fleet.ConfigDriftItem{{Key: "replicas", Desired: "3"}}, "", "run-42", &stateChanged)
	if err != nil {
		t.Fatal(err)
	}
	if got["status"] != fleetConvergenceInSync || got["desired_content_digest"] != "sha256:expected" {
		t.Fatalf("body = %#v", got)
	}
	if got["state_changed"] != true {
		t.Fatalf("state_changed = %#v, want true", got["state_changed"])
	}
	declared := got["declaration"].([]any)
	if len(declared) != 1 || declared[0].(map[string]any)["desired"] != "3" {
		t.Fatalf("declaration = %#v", declared)
	}
}

func TestFleetRunTrackerRecordsTerminalOutcome(t *testing.T) {
	var got struct {
		Status     string `json:"status"`
		ExitCode   *int   `json:"exit_code"`
		ExitReason string `json:"exit_reason"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/fleet/runs/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	tracker := startFleetRunTracker(&cliConfig{Host: srv.URL, Token: "test"}, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := tracker.finish(4, "PARTIAL - 1 failed"); err != nil {
		t.Fatal(err)
	}
	if got.Status != "partial" || got.ExitCode == nil || *got.ExitCode != 4 || got.ExitReason != "PARTIAL - 1 failed" {
		t.Fatalf("terminal update = %+v", got)
	}
}

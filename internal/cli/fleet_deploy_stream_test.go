package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deployevent"
	"github.com/rvben/shinyhub/internal/deployfail"
	"github.com/rvben/shinyhub/internal/fleet"
)

func fleetEventResponse(events ...deployevent.Event) *http.Response {
	var body bytes.Buffer
	for _, event := range events {
		json.NewEncoder(&body).Encode(event)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(&body)}
}

func TestFleetDeployStreamPreservesFailurePhaseRecoveryAndClassification(t *testing.T) {
	resp := fleetEventResponse(
		deployevent.Phase("dependencies", "started", "Building Python dependencies"),
		deployevent.Phase("dependencies", "failed", "Python dependency build failed"),
		deployevent.Phase("recovery", "completed", "Previous deployment remained available"),
		deployevent.Event{Type: "error", Phase: "dependencies", Message: "uv sync failed", StatusCode: 500, FailureKind: "build_failed"},
	)
	var out bytes.Buffer
	_, kind, err := consumeFleetDeployEvents(resp, "demo", &out)
	if kind != deployfail.BuildFailed || err == nil {
		t.Fatalf("kind=%s err=%v", kind, err)
	}
	for _, want := range []string{"failed during dependencies", "uv sync failed", "Previous deployment remained available", "shinyhub apps logs demo --system --tail 200"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q: %v", want, err)
		}
	}
	if !strings.Contains(out.String(), "demo: Building Python dependencies") {
		t.Fatal(out.String())
	}
}

func TestFleetDeployStreamLivePhasesAndWarnings(t *testing.T) {
	var out bytes.Buffer
	d := testFleetDisplay(&out, "demo")
	app, _ := beginFleetApp(d, "demo")
	res := applyResult{}
	warningOut := &resultWarningWriter{Writer: app, result: &res}
	payload := json.RawMessage(`{"status":"ok","manifest":{"schedules":[{"name":"warm","schedule_id":7,"deploy_run":{"run_id":42}}]}}`)
	resp := fleetEventResponse(deployevent.Phase("dependencies", "started", "Building Python dependencies"), deployevent.Phase("hooks", "warning", "Hook skipped by runtime"), deployevent.Phase("replicas", "progress", "Replica 1 started; checking readiness"), deployevent.Event{Type: "result", Result: payload})
	body, kind, err := consumeFleetDeployEvents(resp, "demo", warningOut)
	if err != nil || kind != "" || !bytes.Equal(body, payload) {
		t.Fatalf("kind=%s err=%v body=%s", kind, err, body)
	}
	if d.rows[0].phase != "Replica 1 started; checking readiness" {
		t.Fatalf("row=%+v", d.rows[0])
	}
	if len(res.warnings) != 1 || res.warnings[0] != "Hook skipped by runtime" {
		t.Fatalf("warnings=%v", res.warnings)
	}
	if !strings.Contains(out.String(), "demo: Hook skipped by runtime") {
		t.Fatal(out.String())
	}
	refs := deployRunRefsFromDeployResponse(body)
	if len(refs) != 1 || refs[0].RunID != 42 {
		t.Fatalf("run refs lost: %+v", refs)
	}
}

func TestFleetDeployStreamUnknownOutcomeDoesNotReplayUpload(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/deploy") {
			posts++
			if r.Header.Get("Accept") != deployevent.MediaType {
				t.Error("did not negotiate events")
			}
			w.Header().Set("Content-Type", deployevent.MediaType)
			json.NewEncoder(w).Encode(deployevent.Phase("commit", "started", "Publishing deployment"))
			return // Lost terminal result after a possibly committed deploy.
		}
		io.WriteString(w, `{"app":{"status":"running"}}`)
	}))
	defer srv.Close()
	_, attempts, _, _, _, err := deployWithRetry(&cliConfig{Host: srv.URL}, "demo", bundleBuildSpec{Dir: dir}, "private", "", convergeOpts{retries: 2}, io.Discard, "", "")
	if err == nil || !strings.Contains(err.Error(), "outcome is unknown") || attempts != 1 || posts != 1 {
		t.Fatalf("attempts=%d posts=%d err=%v", attempts, posts, err)
	}
}

func TestFleetDeployNegotiatedSuccessStillVerifiesHealth(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	health := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/deploy"):
			if r.Header.Get("Accept") != deployevent.MediaType {
				t.Error("did not negotiate events")
			}
			w.Header().Set("Content-Type", deployevent.MediaType)
			json.NewEncoder(w).Encode(deployevent.Phase("dependencies", "started", "Building Python dependencies"))
			json.NewEncoder(w).Encode(deployevent.Event{Type: "result", Result: json.RawMessage(`{"status":"ok"}`)})
		case r.URL.Path == "/api/apps":
			io.WriteString(w, `[{"slug":"demo","content_digest":"sha256:new"}]`)
		default:
			health++
			io.WriteString(w, `{"app":{"status":"running"}}`)
		}
	}))
	defer srv.Close()
	var out bytes.Buffer
	digest, committed, _, _, err := deployAppBundle(&cliConfig{Host: srv.URL}, "demo", dir, "private", "", &out, "", time.Second)
	if err != nil || !committed || digest != "sha256:new" || health < 2 {
		t.Fatalf("digest=%s committed=%v health=%d err=%v", digest, committed, health, err)
	}
	if !strings.Contains(out.String(), "Building Python dependencies") {
		t.Fatal(out.String())
	}
}

func TestFleetDeployStreamFailureMutationAndRetryEvidence(t *testing.T) {
	configurationError := deployevent.Event{Type: deployevent.TypeError, Phase: "configuration", Message: "manifest access apply failed", StatusCode: 500}
	for _, tc := range []struct {
		name      string
		events    []deployevent.Event
		mutation  applyMutationState
		attempts  int
		kind      deployfail.Kind
		ambiguous bool
	}{
		{
			name: "configuration_error_after_publication",
			events: []deployevent.Event{
				deployevent.Phase("commit", deployevent.StatusCompleted, "Deployment recorded"),
				configurationError,
			},
			mutation: mutationPartial, attempts: 1, kind: deployfail.ServerError, ambiguous: true,
		},
		{
			name: "configuration_error_after_handoff",
			events: []deployevent.Event{
				deployevent.Phase("handoff", deployevent.StatusCompleted, "New version is ready"),
				configurationError,
			},
			mutation: mutationPartial, attempts: 1, kind: deployfail.ServerError, ambiguous: true,
		},
		{
			name: "lost_result_after_publication",
			events: []deployevent.Event{
				deployevent.Phase("commit", deployevent.StatusCompleted, "Deployment recorded"),
			},
			mutation: mutationPartial, attempts: 1, kind: deployfail.Unknown, ambiguous: true,
		},
		{
			name: "lost_result_during_commit",
			events: []deployevent.Event{
				deployevent.Phase("commit", deployevent.StatusStarted, "Recording the new deployment"),
			},
			mutation: mutationUnknown, attempts: 1, kind: deployfail.Unknown, ambiguous: true,
		},
		{
			name: "explicit_commit_failure",
			events: []deployevent.Event{
				{Type: deployevent.TypeError, Phase: "commit", Message: "publication outcome uncertain", StatusCode: 500},
			},
			mutation: mutationUnknown, attempts: 1, kind: deployfail.ServerError, ambiguous: true,
		},
		{
			name: "invalid_terminal_result",
			events: []deployevent.Event{
				{Type: deployevent.TypeResult, Result: json.RawMessage(`[]`)},
			},
			mutation: mutationUnknown, attempts: 1, kind: deployfail.Unknown, ambiguous: true,
		},
		{
			name: "precommit_server_failure_still_retries",
			events: []deployevent.Event{
				{Type: deployevent.TypeError, Phase: "dependencies", Message: "temporary server failure", StatusCode: 500, FailureKind: string(deployfail.ServerError)},
			},
			mutation: mutationNone, attempts: 3, kind: deployfail.ServerError,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
			posts := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/deploy"):
					posts++
					w.Header().Set("Content-Type", deployevent.MediaType)
					for _, event := range tc.events {
						if err := json.NewEncoder(w).Encode(event); err != nil {
							t.Errorf("encode event: %v", err)
						}
					}
				case strings.HasSuffix(r.URL.Path, "/logs"):
					io.WriteString(w, "deployment diagnostic\n")
				case r.URL.Path == "/api/apps":
					// Even a healthy published app must not turn a failed
					// post-publication configuration step into success.
					io.WriteString(w, `[{"slug":"demo","content_digest":"sha256:new"}]`)
				default:
					io.WriteString(w, `{"app":{"status":"running"}}`)
				}
			}))
			defer srv.Close()
			result := convergeApp(&cliConfig{Host: srv.URL},
				fleet.AppDiff{Slug: "demo", Action: fleet.ActionUpdateSource, Owned: true, ServerDigest: "sha256:old"},
				fleet.AppEntry{Slug: "demo", Visibility: "private"}, fleet.ObservedApp{}, dir,
				convergeOpts{retries: 2, healthTimeout: time.Second}, "fleet:eu", io.Discard)
			if result.err == nil || result.status != statusFailed || result.mutation != tc.mutation || result.attempts != tc.attempts || posts != tc.attempts {
				t.Fatalf("posts=%d result=%+v; want failed mutation=%s attempts=%d", posts, result, tc.mutation, tc.attempts)
			}
			var outcome *fleetDeployOutcomeError
			if got := errors.As(result.err, &outcome); got != tc.ambiguous {
				t.Fatalf("ambiguous=%v, want %v: %v", got, tc.ambiguous, result.err)
			}
			if len(result.attemptsDetail) != tc.attempts || result.attemptsDetail[0].Kind != tc.kind {
				t.Fatalf("failure classification lost: %+v", result.attemptsDetail)
			}
		})
	}
}

func TestFleetDeployStreamAdoptKeepsPublishedOwnership(t *testing.T) {
	for _, publicationReported := range []bool{false, true} {
		name := "digest_readback_proves_publication"
		if publicationReported {
			name = "stream_proves_publication"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
			patches, posts, readbacks := 0, 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPatch:
					patches++
					io.WriteString(w, `{}`)
				case strings.HasSuffix(r.URL.Path, "/deploy"):
					posts++
					w.Header().Set("Content-Type", deployevent.MediaType)
					status := deployevent.StatusStarted
					if publicationReported {
						status = deployevent.StatusCompleted
					}
					json.NewEncoder(w).Encode(deployevent.Phase("commit", status, "Recording deployment"))
					// The terminal result is lost; only the stream or digest
					// readback can prove the ownership reservation must remain.
				case r.URL.Path == "/api/apps":
					readbacks++
					io.WriteString(w, `[{"slug":"demo","content_digest":"sha256:new"}]`)
				case strings.HasSuffix(r.URL.Path, "/logs"):
					io.WriteString(w, "deployment diagnostic\n")
				default:
					io.WriteString(w, `{"app":{"status":"running"}}`)
				}
			}))
			defer srv.Close()
			result := convergeApp(&cliConfig{Host: srv.URL},
				fleet.AppDiff{Slug: "demo", Action: fleet.ActionAdopt, ServerDigest: "sha256:old", LocalDigest: "sha256:new"},
				fleet.AppEntry{Slug: "demo", Visibility: "private"}, fleet.ObservedApp{}, dir,
				convergeOpts{adopt: true, preconditions: true, retries: 2}, "fleet:eu", io.Discard)
			if result.err == nil || result.status != statusFailed || result.mutation != mutationPartial || result.attempts != 1 || posts != 1 || patches != 1 {
				t.Fatalf("published ownership released or evidence lost: posts=%d patches=%d result=%+v", posts, patches, result)
			}
			if publicationReported && readbacks != 0 || !publicationReported && readbacks != 1 {
				t.Fatalf("readbacks=%d for publicationReported=%v", readbacks, publicationReported)
			}
		})
	}
}

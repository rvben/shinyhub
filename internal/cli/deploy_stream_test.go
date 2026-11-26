package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/deployevent"
)

func deployEventResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{deployevent.MediaType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestConsumeDeployEventsHumanProgressAndResult(t *testing.T) {
	resp := deployEventResponse(strings.Join([]string{
		`{"type":"phase","phase":"dependencies","status":"started","message":"Building Python dependencies"}`,
		`{"type":"phase","phase":"dependencies","status":"completed","message":"Dependencies ready"}`,
		`{"type":"result","result":{"deploy_count":2,"status":"running"}}`,
	}, "\n"))
	var progress bytes.Buffer
	out, err := consumeDeployEvents(resp, formatTable, io.Discard, &progress, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != `{"deploy_count":2,"status":"running"}` {
		t.Fatalf("result = %s", got)
	}
	for _, want := range []string{"Building Python dependencies", "Dependencies ready"} {
		if !strings.Contains(progress.String(), want) {
			t.Fatalf("progress missing %q:\n%s", want, progress.String())
		}
	}
}

// A deploy queued behind the host's build gate reports the wait as its own
// phase. Off a terminal, where progress heartbeats without elapsed time are
// dropped, the start and completion of that phase must still print, or a
// queued deploy in CI looks stalled.
func TestConsumeDeployEventsShowsBuildQueueWaitOffTerminal(t *testing.T) {
	resp := deployEventResponse(strings.Join([]string{
		`{"type":"phase","phase":"build_queue","status":"started","message":"Waiting for a build slot (2 of 2 running, 1 queued)"}`,
		`{"type":"phase","phase":"build_queue","status":"progress","message":"Still waiting for a build slot (2 of 2 running, 1 queued)","elapsed_seconds":15}`,
		`{"type":"phase","phase":"build_queue","status":"completed","message":"Build slot acquired after 22s","elapsed_seconds":22}`,
		`{"type":"phase","phase":"dependencies","status":"started","message":"Building python dependencies (budget 15m0s)"}`,
		`{"type":"phase","phase":"dependencies","status":"completed","message":"Dependencies ready"}`,
		`{"type":"result","result":{"deploy_count":1,"status":"deployed"}}`,
	}, "\n"))
	var progress bytes.Buffer
	if _, err := consumeDeployEvents(resp, formatTable, io.Discard, &progress, false); err != nil {
		t.Fatal(err)
	}
	got := progress.String()
	for _, want := range []string{
		"Waiting for a build slot (2 of 2 running, 1 queued)",
		"Still waiting for a build slot (2 of 2 running, 1 queued)",
		"Build slot acquired after 22s",
		"Building python dependencies",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress missing %q:\n%s", want, got)
		}
	}
	if queue, build := strings.Index(got, "Build slot acquired"), strings.Index(got, "Building python dependencies"); !(queue < build) {
		t.Fatalf("the queue wait must be reported before the build it delayed:\n%s", got)
	}
}

func TestConsumeDeployEventsNDJSONFailureIsTerminalAndNotDuplicated(t *testing.T) {
	resp := deployEventResponse(strings.Join([]string{
		`{"type":"phase","phase":"dependencies","status":"failed","message":"Python dependency build failed"}`,
		`{"type":"error","phase":"dependencies","message":"deploy failed: uv sync: package missing","status_code":500,"failure_kind":"build_failed"}`,
	}, "\n"))
	var out bytes.Buffer
	_, err := consumeDeployEvents(resp, formatNDJSON, &out, io.Discard, false)
	if err == nil {
		t.Fatal("expected terminal error")
	}
	var coded *ExitCodeError
	if !errors.As(err, &coded) || !coded.Reported || coded.Code != 3 {
		t.Fatalf("error = %#v", err)
	}
	if lines := strings.Count(strings.TrimSpace(out.String()), "\n") + 1; lines != 2 {
		t.Fatalf("want two unchanged events, got %d:\n%s", lines, out.String())
	}
	if !strings.Contains(err.Error(), "during dependencies") {
		t.Fatalf("error does not name phase: %v", err)
	}
}

func TestDeployEventContentTypeAllowsCharset(t *testing.T) {
	resp := deployEventResponse("")
	resp.Header.Set("Content-Type", deployevent.MediaType+"; charset=utf-8")
	if !isDeployEventResponse(resp) {
		t.Fatal("negotiated response not recognized")
	}
}

func TestResolveDeployFormatAllowsExplicitNDJSONButKeepsPipedJSONDefault(t *testing.T) {
	oldOutput, oldResolved := outputFlagValue, resolvedFormat
	t.Cleanup(func() { outputFlagValue, resolvedFormat = oldOutput, oldResolved })

	outputFlagValue = "ndjson"
	if got, err := resolveDeployFormat(); err != nil || got != formatNDJSON {
		t.Fatalf("explicit ndjson = %q, %v", got, err)
	}
	outputFlagValue, resolvedFormat = "", ""
	if got, err := resolveDeployFormat(); err != nil || got != formatJSON {
		t.Fatalf("piped default = %q, %v", got, err)
	}
}

func TestStreamedCapacityIsDeferredRatherThanStaleSavedPlan(t *testing.T) {
	response := deployEventResponse(`{"type":"error","phase":"capacity","status_code":409,"failure_kind":"capacity","message":"working version preserved","capacity_retryable":true,"retry_after_seconds":60}`)
	_, err := consumeDeployEvents(response, formatTable, io.Discard, io.Discard, false)
	var capacity *fleetCapacityError
	var status *httpStatusError
	if !errors.As(err, &capacity) || errors.As(err, &status) {
		t.Fatalf("capacity can be mistaken for a stale saved plan: %v", err)
	}
}

func TestConsumeDeployEventsUnknownOutcome(t *testing.T) {
	for _, body := range []string{"", `{"type":`, `{"type":"result","result":null}`, `{"type":"result","result":[]}`} {
		t.Run(body, func(t *testing.T) {
			_, err := consumeDeployEvents(deployEventResponse(body), formatTable, io.Discard, io.Discard, true, "forecast")
			if err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
				t.Fatalf("error = %v", err)
			}
			var hinted *hintedMsgError
			if !errors.As(err, &hinted) || !strings.Contains(hinted.hint, "shinyhub apps show forecast") || !strings.Contains(hinted.hint, "before deploying again") {
				t.Fatalf("missing recovery hint: %#v", err)
			}
		})
	}
}

func TestConsumeDeployEventsInterruptedAfterPublication(t *testing.T) {
	_, err := consumeDeployEvents(deployEventResponse(`{"type":"phase","phase":"commit","status":"completed"}`), formatTable, io.Discard, io.Discard, true, "forecast")
	if err == nil || !strings.Contains(err.Error(), "was published") {
		t.Fatalf("error = %v", err)
	}
}

// The exact hints a deploy failure carries. They are spelled out here rather
// than taken from deployFailureHint, so a test cannot pass with the retry
// advice reworded into something false.
const (
	wantBuildFailedHint    = "build_failed: the server could not install the bundle's dependencies, for the reason the message gives. A timeout or an unreachable package index can clear on a retry; a dependency conflict usually does not"
	wantRuntimeMissingHint = "runtime_missing: an executable the app needs was not found where it runs; a retry fails the same way until it is available there"
)

// assertDeployFailureEnvelope renders err as a machine-readable failure and
// checks the envelope keeps the server's message, kind server_error and exit
// code 3 for a 5xx (validation and 1 for a 422), and carries exactly hint.
func assertDeployFailureEnvelope(t *testing.T, err error, status int, message, hint string) {
	t.Helper()
	wantKind, wantCode := KindServerError, 3
	if status == http.StatusUnprocessableEntity {
		wantKind, wantCode = KindValidation, 1
	}
	var envelope bytes.Buffer
	if code := reportTo(&envelope, false, formatJSON, err); code != wantCode {
		t.Errorf("exit code = %d, want %d: the hint must not change it", code, wantCode)
	}
	var env errEnvelope
	if jerr := json.Unmarshal(envelope.Bytes(), &env); jerr != nil {
		t.Fatalf("envelope %q: %v", envelope.String(), jerr)
	}
	if env.Error.Kind != string(wantKind) || env.Error.ExitCode != wantCode {
		t.Errorf("envelope kind/exit_code = %s/%d, want %s/%d", env.Error.Kind, env.Error.ExitCode, wantKind, wantCode)
	}
	if env.Error.Message != message {
		t.Errorf("message = %q, want the server's message unchanged: %q", env.Error.Message, message)
	}
	if env.Error.Hint != hint {
		t.Errorf("hint = %q, want %q", env.Error.Hint, hint)
	}
}

// A deploy that fails on the server with a failure kind a retry may not clear
// still reports server_error with exit code 3, so its envelope carries a hint
// naming the kind and what a retry can and cannot clear. A kind a retry can
// clear, one whose causes include a transient server error, an unclassified
// one, and a 4xx carry no hint.
func TestConsumeDeployEventsHintsAtFailureKindARetryMayNotClear(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		kind     string
		wantHint string
	}{
		{"build failed", 500, "build_failed", wantBuildFailedHint},
		{"runtime missing", 500, "runtime_missing", wantRuntimeMissingHint},
		{"bundle invalid includes transient read errors", 500, "bundle_invalid", ""},
		{"hook failed can be transient", 500, "hook_failed", ""},
		{"interpreter download can be transient", 500, "interpreter_unavailable", ""},
		{"crashed", 500, "crashed", ""},
		{"readiness timeout", 500, "readiness_timeout", ""},
		{"unclassified", 500, "server_error", ""},
		{"a kind this CLI does not know", 500, "some_future_kind", ""},
		{"no kind from an older server", 500, "", ""},
		{"a client error keeps its own classification", 422, "build_failed", ""},
	}
	for _, tc := range cases {
		for _, format := range []outputFormat{formatTable, formatNDJSON} {
			t.Run(tc.name+"/"+string(format), func(t *testing.T) {
				event := fmt.Sprintf(`{"type":"error","phase":"producer_environment","message":"deploy failed: boom","status_code":%d,"failure_kind":%q}`, tc.status, tc.kind)
				_, err := consumeDeployEvents(deployEventResponse(event), format, io.Discard, io.Discard, true)
				if err == nil {
					t.Fatal("expected terminal error")
				}
				assertDeployFailureEnvelope(t, err, tc.status, "deploy failed during producer_environment: deploy failed: boom", tc.wantHint)
			})
		}
	}
}

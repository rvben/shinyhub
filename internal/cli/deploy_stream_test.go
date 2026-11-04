package cli

import (
	"bytes"
	"errors"
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

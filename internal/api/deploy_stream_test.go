package api

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/deployevent"
	"github.com/rvben/shinyhub/internal/deployfail"
)

func TestDeployResponderPreservesLegacyJSONResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/deploy", nil)
	responder := newDeployResponder(rec, req)
	responder.event(deployevent.Phase("dependencies", deployevent.StatusStarted, "building"))
	responder.result(map[string]any{"status": "running"})

	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status/content-type = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"status":"running"}` {
		t.Fatalf("legacy body = %s", got)
	}
}

func TestDeployResponderStreamsAndPreservesFailedPhase(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/deploy", nil)
	req.Header.Set("Accept", "application/json, "+deployevent.MediaType)
	responder := newDeployResponder(rec, req)
	responder.event(deployevent.Phase("dependencies", deployevent.StatusStarted, "building"))
	responder.event(deployevent.Phase("dependencies", deployevent.StatusFailed, "build failed"))
	responder.event(deployevent.Phase("recovery", deployevent.StatusCompleted, "previous version restored"))
	responder.fail(http.StatusInternalServerError, "uv sync failed", deployfail.BuildFailed, "deploy")

	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != deployevent.MediaType {
		t.Fatalf("status/content-type = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var events []deployevent.Event
	scanner := bufio.NewScanner(rec.Body)
	for scanner.Scan() {
		var event deployevent.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	terminal := events[len(events)-1]
	if terminal.Type != deployevent.TypeError || terminal.Phase != "dependencies" || terminal.FailureKind != "build_failed" {
		t.Fatalf("terminal event = %#v", terminal)
	}
}

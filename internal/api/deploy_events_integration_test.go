package api_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/deployevent"
)

func eventDeployRequest(t *testing.T, srv interface{ Router() http.Handler }, token, slug string) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := buildBundleUpload(t, "app.py", "print('events')\n")
	req := httptest.NewRequest(http.MethodPost, "/api/apps/"+slug+"/deploy", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", deployevent.MediaType)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func decodeDeployEvents(t *testing.T, rec *httptest.ResponseRecorder) []deployevent.Event {
	t.Helper()
	var events []deployevent.Event
	scanner := bufio.NewScanner(rec.Body)
	for scanner.Scan() {
		var event deployevent.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode event %q: %v", scanner.Text(), err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func seedEventDeployApp(t *testing.T, store *db.Store, slug string) string {
	t.Helper()
	hash, _ := testHashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByUsername("admin")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	return token
}

func TestDeployEventsNegotiationStreamsLifecycleAndOriginalResult(t *testing.T) {
	srv, store := newQuotaTestServer(t, t.TempDir(), 0)
	token := seedEventDeployApp(t, store, "event-ok")
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		p.Progress(deployevent.Phase("dependencies", deployevent.StatusStarted, "Building Python dependencies"))
		p.Progress(deployevent.Phase("dependencies", deployevent.StatusCompleted, "Dependencies ready"))
		return &deploy.PoolResult{Replicas: []deploy.Result{{Index: 0, PID: 1, Port: 20001}}}, nil
	})

	rec := eventDeployRequest(t, srv, token, "event-ok")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != deployevent.MediaType {
		t.Fatalf("status/content-type = %d %q: %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	events := decodeDeployEvents(t, rec)
	if len(events) < 6 {
		t.Fatalf("expected lifecycle events, got %#v", events)
	}
	terminal := events[len(events)-1]
	if terminal.Type != deployevent.TypeResult || len(terminal.Result) == 0 {
		t.Fatalf("terminal event = %#v", terminal)
	}
	var result map[string]any
	if err := json.Unmarshal(terminal.Result, &result); err != nil || result["deploy_count"] != float64(1) {
		t.Fatalf("normal deploy result was not preserved: %#v, %v", result, err)
	}
}

func TestDeployEventsFailureNamesEnginePhaseAndRetainsFailureKind(t *testing.T) {
	srv, store := newQuotaTestServer(t, t.TempDir(), 0)
	token := seedEventDeployApp(t, store, "event-fail")
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		p.Progress(deployevent.Phase("dependencies", deployevent.StatusStarted, "Building Python dependencies"))
		p.Progress(deployevent.Phase("dependencies", deployevent.StatusFailed, "Python dependency build failed"))
		return nil, errors.New("uv sync: package not found")
	})

	rec := eventDeployRequest(t, srv, token, "event-fail")
	if rec.Code != http.StatusOK {
		t.Fatalf("a begun stream must retain HTTP 200, got %d", rec.Code)
	}
	events := decodeDeployEvents(t, rec)
	terminal := events[len(events)-1]
	if terminal.Type != deployevent.TypeError || terminal.Phase != "dependencies" || terminal.FailureKind != "build_failed" || terminal.StatusCode != 500 {
		t.Fatalf("terminal event = %#v", terminal)
	}
}

func TestDeployEventsNegotiationKeepsEarlyValidationHTTPStatus(t *testing.T) {
	srv, store := newQuotaTestServer(t, t.TempDir(), 0)
	token := seedEventDeployApp(t, store, "event-bad")
	req := httptest.NewRequest(http.MethodPost, "/api/apps/event-bad/deploy", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", deployevent.MediaType)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || rec.Header().Get("Content-Type") == deployevent.MediaType {
		t.Fatalf("early validation changed protocol: %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

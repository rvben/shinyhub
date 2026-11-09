package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A store failure inside a handler answers 500 with a short description and
// the request id, never the store's own error text, which names tables and
// columns.
func TestScheduleListStoreFailureKeepsTheCauseOutOfTheBody(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	_, token := scheduleWithTimezone(t, store, "")
	if _, err := store.DB().Exec(`ALTER TABLE app_schedules RENAME TO app_schedules_unavailable`); err != nil {
		t.Fatal(err)
	}

	req := authedRequest(t, http.MethodGet, "/api/apps/tz-app/schedules", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "app_schedules") || strings.Contains(rec.Body.String(), "no such table") {
		t.Fatalf("body leaks the store error: %s", rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, rec.Body.String())
	}
	if body["error"] == "" {
		t.Fatalf("body = %v, want a description of what failed", body)
	}
	if id := rec.Header().Get("X-Request-Id"); id == "" || body["request_id"] != id {
		t.Fatalf("request_id = %q, X-Request-Id = %q; want the same non-empty id", body["request_id"], id)
	}
}

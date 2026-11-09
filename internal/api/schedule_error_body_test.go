package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A request whose JSON key holds a control character reaches the client in the
// rejection message, so the rejection itself must still be valid JSON.
func TestSchedulePatchRejectsAnUnknownFieldWithValidJSON(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	schedID, token := scheduleWithTimezone(t, store, "Europe/Amsterdam")

	req := authedRequest(t, "PATCH", fmt.Sprintf("/api/apps/tz-app/schedules/%d", schedID), []byte(`{"nam\u007fe":"x"}`), token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("rejection is not JSON: %v\n%s", err, rec.Body.String())
	}
	if want := "unknown schedule field: nam\x7fe"; body["error"] != want {
		t.Fatalf("error = %q, want %q", body["error"], want)
	}
}

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The cause of a server error belongs in the log, joined to the request by its
// id, and never in the body, where it can expose SQL text or server paths.
func TestWriteInternalLogsTheCauseAndAnswersWithoutIt(t *testing.T) {
	logs, restore := captureLogs(t)
	defer restore()
	cause := errors.New(`sqlite: no such column "secret_col" in /srv/shinyhub/data/shinyhub.db`)
	r := httptest.NewRequest(http.MethodPost, "/api/apps/sales/schedules", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxKeyRequestID{}, "req-42"))
	rec := httptest.NewRecorder()

	writeInternal(rec, r, "plan schedule convergence", cause)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret_col") || strings.Contains(rec.Body.String(), "/srv/") {
		t.Fatalf("body leaks the cause: %s", rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, rec.Body.String())
	}
	if body["error"] != "plan schedule convergence" || body["request_id"] != "req-42" {
		t.Fatalf("body = %v, want the message and request id", body)
	}

	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("want exactly one JSON log record, got %q: %v", logs.String(), err)
	}
	for key, want := range map[string]string{
		"level":      "ERROR",
		"request_id": "req-42",
		"error":      "plan schedule convergence",
		"err":        cause.Error(),
		"method":     http.MethodPost,
		"path":       "/api/apps/sales/schedules",
	} {
		if got, _ := entry[key].(string); got != want {
			t.Errorf("log %s = %q, want %q (record %v)", key, got, want, entry)
		}
	}
}

// Without a request id (a handler driven directly), the body carries no empty
// request_id field, and the cause is still logged.
func TestWriteInternalOmitsAMissingRequestID(t *testing.T) {
	logs, restore := captureLogs(t)
	defer restore()
	r := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
	rec := httptest.NewRecorder()

	writeInternal(rec, r, "internal server error", errors.New("database is locked"))

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if _, ok := body["request_id"]; ok {
		t.Fatalf("body = %v, want no request_id", body)
	}
	if !strings.Contains(logs.String(), "database is locked") {
		t.Fatalf("cause not logged: %s", logs.String())
	}
}

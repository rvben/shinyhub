package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecoverAPI_LogsRecoveredPanic(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	h := recoverAPI(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("patch exploded")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("PATCH", "/api/apps/demo", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	for _, want := range []string{`"msg":"panic serving API request"`, `"method":"PATCH"`, `"path":"/api/apps/demo"`, `patch exploded`} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("panic log missing %s: %s", want, logs.String())
		}
	}
}

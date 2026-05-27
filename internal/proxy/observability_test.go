package proxy_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/proxy"
)

func captureSlog(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return &buf, func() { slog.SetDefault(prev) }
}

func findSlogRecord(t *testing.T, logs, msg string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // ignore non-JSON lines from other writers
		}
		if rec["msg"] == msg {
			return rec
		}
	}
	t.Fatalf("no %q record found in slog output:\n%s", msg, logs)
	return nil
}

// TestProxy_LogsUpstreamErrorStructured proves a pre-response upstream failure
// (connection refused) is reported through the structured slog pipeline with
// the slug and error as fields, not as an unstructured stderr line. This keeps
// the proxy's error reporting on the same log stream a log aggregator ingests.
func TestProxy_LogsUpstreamErrorStructured(t *testing.T) {
	// Stand up a backend then close it so its URL refuses connections.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := backend.URL
	backend.Close()

	p := proxy.New()
	if err := p.Register("dead", deadURL); err != nil {
		t.Fatalf("register: %v", err)
	}

	buf, restore := captureSlog(t)
	defer restore()

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/app/dead/", nil))

	r := findSlogRecord(t, buf.String(), "proxy_upstream_error")
	if r["slug"] != "dead" {
		t.Fatalf("upstream-error log slug = %v, want dead", r["slug"])
	}
	if _, ok := r["error"]; !ok {
		t.Fatalf("upstream-error log missing error field: %v", r)
	}
}

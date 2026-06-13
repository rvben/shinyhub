package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

const metricsBody = `{
  "status": "running",
  "sessions_cap": 10,
  "metrics_available": true,
  "replicas": [
    {"index":0,"status":"running","pid":4321,"cpu_percent":12.5,"rss_bytes":104857600,"sessions":3,"tier":"local","provider":"native","metrics_available":true},
    {"index":1,"status":"running","pid":4322,"cpu_percent":3.0,"rss_bytes":52428800,"sessions":1,"tier":"local","provider":"native","metrics_available":true}
  ]
}`

func TestAppsMetrics_Table(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, metricsBody)

	out, err := execCLI(t, "apps", "metrics", "demo", "-o", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r := (*reqs)[0]; r.Method != "GET" || r.Path != "/api/apps/demo/metrics" {
		t.Fatalf("expected GET /api/apps/demo/metrics, got %s %s", r.Method, r.Path)
	}
	for _, want := range []string{"REPLICA", "SESSIONS", "PLACEMENT", "sessions cap 10", "4321", "local/native"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q:\n%s", want, out)
		}
	}
	// Both replicas rendered.
	if strings.Count(out, "running") < 2 {
		t.Errorf("expected a row per replica:\n%s", out)
	}
}

func TestAppsMetrics_JSONPassthrough(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, metricsBody)

	out, err := execCLI(t, "apps", "metrics", "demo") // piped => JSON
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var obj map[string]any
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &obj); jerr != nil {
		t.Fatalf("metrics JSON not valid: %v\n%q", jerr, out)
	}
	if _, ok := obj["replicas"]; !ok {
		t.Errorf("JSON output should pass through the replicas array:\n%s", out)
	}
}

func TestAppsMetrics_404NotFound(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(404, `{"error":"app not found"}`)

	_, err := execCLI(t, "apps", "metrics", "ghost")
	if err == nil {
		t.Fatal("expected an error for 404")
	}
	if kind, code := classify(err); kind != KindNotFound || code != 1 {
		t.Errorf("404 should be not_found/exit 1, got kind=%q code=%d", kind, code)
	}
}

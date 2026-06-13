package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestAppsTableAligns verifies the apps-list columns stay aligned even when a
// slug is wider than the historical fixed column width.
func TestAppsTableAligns(t *testing.T) {
	var buf bytes.Buffer
	items := []map[string]any{
		{"slug": "dakota-first-dashboard", "status": "running", "deploy_count": 1.0},
		{"slug": "morgan", "status": "stopped", "deploy_count": 2.0},
	}
	writeAppsTable(&buf, items)

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want header + 2 rows, got %d lines: %q", len(lines), buf.String())
	}
	statusCol := strings.Index(lines[0], "STATUS")
	if statusCol < 0 {
		t.Fatalf("no STATUS header in %q", lines[0])
	}
	wantStatus := []string{"running", "stopped"}
	for i, ln := range lines[1:] {
		if got := strings.Index(ln, wantStatus[i]); got != statusCol {
			t.Errorf("row %q: status starts at %d, want %d (aligned with header)", ln, got, statusCol)
		}
	}
}

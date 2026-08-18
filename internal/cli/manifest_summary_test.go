package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestFormatManifestSummary_FromServerShape asserts the CLI summary lines
// match what the server emits in the deploy response's "manifest" field.
// The fixture is a literal JSON payload (not a hand-built Go map) so
// changes to the server's field names or numeric encoding surface here.
func TestFormatManifestSummary_FromServerShape(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "no manifest key",
			body: `{"slug":"demo","deploy_count":3}`,
			want: nil,
		},
		{
			name: "app only",
			body: `{"manifest":{"app":{"replicas":2,"max_sessions_per_replica":10}}}`,
			want: []string{"Applied [app] settings: max_sessions_per_replica=10; replicas=2"},
		},
		{
			name: "hibernate reset (null)",
			body: `{"manifest":{"app":{"hibernate_timeout_minutes":null}}}`,
			want: []string{"Applied [app] settings: hibernate_timeout_minutes=default"},
		},
		{
			name: "autoscale enabled renders compactly",
			body: `{"manifest":{"app":{"autoscale":{"enabled":true,"min_replicas":1,"max_replicas":8,"target":0.8}}}}`,
			want: []string{"Applied [app] settings: autoscale=on (1-8 @ 0.80)"},
		},
		{
			name: "autoscale disabled renders off",
			body: `{"manifest":{"app":{"autoscale":{"enabled":false,"min_replicas":0,"max_replicas":0,"target":0}}}}`,
			want: []string{"Applied [app] settings: autoscale=off"},
		},
		{
			name: "autoscale target 0 inherits default",
			body: `{"manifest":{"app":{"autoscale":{"enabled":true,"min_replicas":2,"max_replicas":4,"target":0}}}}`,
			want: []string{"Applied [app] settings: autoscale=on (2-4 @ default)"},
		},
		{
			name: "schedules only",
			body: `{"manifest":{"schedules":[{"name":"a","action":"created"},{"name":"b","action":"updated"}]}}`,
			want: []string{"Schedules: 1 created, 1 updated"},
		},
		{
			name: "both",
			body: `{"manifest":{"app":{"replicas":3},"schedules":[{"name":"x","action":"created"}]}}`,
			want: []string{
				"Applied [app] settings: replicas=3",
				"Schedules: 1 created, 0 updated",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp map[string]any
			if err := json.Unmarshal([]byte(tt.body), &resp); err != nil {
				t.Fatal(err)
			}
			got := formatManifestSummary(resp["manifest"])
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFormatManifestSummaryIconShadowWarning asserts formatManifestSummary
// appends a recovery-pointing warning when the server reports a declared
// manifest icon shadowed an already-uploaded image, and stays silent when the
// flag is absent. The flag sits outside the "app" map (formatAppFields splats
// every key of that map into the "Applied [app] settings:" line), so it must
// never leak into that line.
func TestFormatManifestSummaryIconShadowWarning(t *testing.T) {
	emoji := "\U0001F4CA"
	raw := map[string]any{
		"icon_shadowed_upload": true,
		"app":                  map[string]any{"icon": emoji},
	}
	lines := formatManifestSummary(raw)

	var warning string
	for _, line := range lines {
		if strings.Contains(line, "still stored") {
			warning = line
		}
	}
	if warning == "" {
		t.Fatalf("expected a warning line noting the retained image, got %q", lines)
	}
	if !strings.Contains(warning, `icon = ""`) {
		t.Errorf("warning must name the recovery action (icon = \"\"), got %q", warning)
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "Applied [app] settings:") && strings.Contains(line, "icon_shadowed_upload") {
			t.Errorf("the [app] settings line must not leak icon_shadowed_upload, got %q", line)
		}
	}

	// Absent flag: no warning line.
	rawNoFlag := map[string]any{"app": map[string]any{"icon": emoji}}
	for _, line := range formatManifestSummary(rawNoFlag) {
		if strings.Contains(line, "still stored") {
			t.Errorf("no warning expected when icon_shadowed_upload is absent, got %q", line)
		}
	}
}

// TestFormatHooksSkippedWarning_FromServerShape asserts the CLI surfaces the
// server's hooks_skipped count as a developer-facing warning, and stays silent
// when no hooks were skipped. The fixture is the literal deploy-response shape.
func TestFormatHooksSkippedWarning_FromServerShape(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "no hooks_skipped key",
			body: `{"slug":"demo","deploy_count":1}`,
			want: "",
		},
		{
			name: "zero is silent",
			body: `{"hooks_skipped":0}`,
			want: "",
		},
		{
			name: "single hook",
			body: `{"hooks_skipped":1}`,
			want: "Warning: 1 post-deploy hook skipped under the container runtime; bake setup into the image instead.",
		},
		{
			name: "multiple hooks pluralize",
			body: `{"hooks_skipped":3}`,
			want: "Warning: 3 post-deploy hooks skipped under the container runtime; bake setup into the image instead.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp map[string]any
			if err := json.Unmarshal([]byte(tt.body), &resp); err != nil {
				t.Fatal(err)
			}
			got := formatHooksSkippedWarning(resp["hooks_skipped"])
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatHookExecutionSummary(t *testing.T) {
	var resp map[string]any
	if err := json.Unmarshal([]byte(`{"hooks_declared":2,"hooks_run":2}`), &resp); err != nil {
		t.Fatal(err)
	}
	if got := formatHookExecutionSummary(resp); got != "Post-deploy hooks: 2 declared, 2 ran, 0 skipped" {
		t.Errorf("summary = %q", got)
	}
	if got := formatHookExecutionSummary(map[string]any{}); got != "" {
		t.Errorf("old/no-hook response should stay silent, got %q", got)
	}
}

// TestFormatManifestSummaryWarnings asserts the manifest block's warnings list
// is rendered as one "Note: ..." line each, placed with the other advisories,
// and that an absent or malformed field stays silent (older servers omit it).
func TestFormatManifestSummaryWarnings(t *testing.T) {
	raw := map[string]any{
		"app": map[string]any{"min_warm_replicas": float64(1)},
		"warnings": []any{
			"min_warm_replicas=1 has no effect under worker.isolation=grouped",
			"",
			42,
		},
	}
	lines := formatManifestSummary(raw)
	want := []string{
		"Applied [app] settings: min_warm_replicas=1",
		"Note: min_warm_replicas=1 has no effect under worker.isolation=grouped",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("got %q, want %q", lines, want)
	}

	if got := formatManifestWarnings(map[string]any{"app": map[string]any{}}); got != nil {
		t.Errorf("absent warnings must be silent, got %q", got)
	}
	if got := formatManifestWarnings(map[string]any{"warnings": "not a list"}); got != nil {
		t.Errorf("malformed warnings must be silent, got %q", got)
	}
	if got := formatManifestWarnings(nil); got != nil {
		t.Errorf("nil manifest must be silent, got %q", got)
	}
}

package cli

import (
	"encoding/json"
	"reflect"
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

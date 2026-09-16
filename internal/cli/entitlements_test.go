package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestEntitlementCLIGrantSourcesAndErrors(t *testing.T) {
	for _, tc := range []struct{ command, method, field, value string }{
		{"grant", "POST", "username", "analyst"},
		{"revoke", "DELETE", "username", "analyst"},
		{"group-grant", "POST", "group", "finance/team"},
		{"group-revoke", "DELETE", "group", "finance/team"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			resetFormatState(t)
			outputFlagValue = "table"
			_, requests := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
			cmd := newAppsCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs([]string{"entitlements", tc.command, "report", "power_user", tc.value})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if len(*requests) != 1 {
				t.Fatalf("got %d requests, want 1", len(*requests))
			}
			request := (*requests)[0]
			if request.Method != tc.method || request.Path != "/api/apps/report/entitlements/power_user/grants" {
				t.Errorf("request = %s %s", request.Method, request.Path)
			}
			var body map[string]string
			if err := json.Unmarshal(request.Body, &body); err != nil {
				t.Fatal(err)
			}
			if body[tc.field] != tc.value || len(body) != 1 {
				t.Errorf("principal = %v", body)
			}
			if tc.method == "DELETE" && !strings.Contains(out.String(), "other grant sources") {
				t.Fatalf("revoke implies privilege removed: %s", out.String())
			}
		})
	}
	t.Run("server rejects grant", func(t *testing.T) {
		setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "forbidden", http.StatusForbidden) })
		cmd := newAppsCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"entitlements", "grant", "report", "power_user", "analyst"})
		if err := cmd.Execute(); err == nil {
			t.Fatal("failed grant reported success")
		}
	})
}

func TestEntitlementCLIDefinePreservesOmittedDescription(t *testing.T) {
	for _, tc := range []struct {
		name        string
		flags       []string
		present     bool
		description string
	}{
		{name: "omit"},
		{name: "set", flags: []string{"--description", "Advanced reports"}, present: true, description: "Advanced reports"},
		{name: "clear", flags: []string{"--description", ""}, present: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetFormatState(t)
			_, requests := setupCLITestHandler(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
			cmd := newAppsCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs(append([]string{"entitlements", "define", "report", "power_user"}, tc.flags...))
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if len(*requests) != 1 {
				t.Fatalf("got %d requests, want 1", len(*requests))
			}
			var body map[string]string
			if err := json.Unmarshal((*requests)[0].Body, &body); err != nil {
				t.Fatal(err)
			}
			description, present := body["description"]
			if present != tc.present || description != tc.description {
				t.Fatalf("description = %q present=%v, want %q present=%v", description, present, tc.description, tc.present)
			}
		})
	}
}

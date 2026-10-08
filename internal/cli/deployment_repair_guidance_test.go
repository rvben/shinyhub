package cli

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompatibilityQuarantineReportsDeploymentCause(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		contains []string
		command  bool
	}{
		{"failed deployment", `{"compatibility_quarantined":true,"producer_repair_required":false,"app":{"deployment_repair_required":true,"last_deployment_status":"failed","current_version":"bad-version","last_deployed_at":"2026-10-08T08:00:00Z","last_error":"import failed"}}`, []string{"bad-version", "2026-10-08T08:00:00Z", "import failed", "successful producers alone", "shinyhub deploy", "--allow-downtime"}, true},
		{"pending deployment", `{"compatibility_quarantined":true,"producer_repair_required":false,"app":{"deployment_repair_required":true,"last_deployment_status":"pending","current_version":"pending-version"}}`, []string{"pending-version", "pending", "wait for it to finish"}, false},
		{"writer failure", `{"compatibility_quarantined":true,"producer_repair_required":true,"app":{"deployment_repair_required":false}}`, []string{"successful producer repair"}, false},
		{"running writer", `{"compatibility_quarantined":true,"producer_repair_required":false,"app":{"deployment_repair_required":false}}`, []string{"active data producers"}, false},
		{"older server", `{"compatibility_quarantined":true,"producer_repair_required":false,"app":{"last_deployment_status":"failed"}}`, []string{"does not report deployment repair state", "If the failed deployment entered", "unchanged apply alone"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(srv.Close)
			err := requireAppCompatibilityClear(&cliConfig{Host: srv.URL, Token: "shk_test"}, "app")
			var quarantine *appCompatibilityQuarantineError
			if !errors.As(err, &quarantine) {
				t.Fatalf("error=%v", err)
			}
			for _, want := range tc.contains {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("missing %q in %s", want, err)
				}
			}
			if strings.Contains(err.Error(), "incomplete producer barrier") || (quarantine.repairCommand != "") != tc.command {
				t.Fatalf("misleading barrier diagnosis: %+v", quarantine)
			}
			recovery := applyRecoveryFor(recoveryCtx(), []applyResult{{slug: "app", status: statusFailed, failureKind: failureWarmNeverSucceeded, err: err}})
			if tc.command && (!strings.Contains(recovery.Summary, "corrective deployment") || len(recovery.Commands) != 2 || !strings.HasPrefix(recovery.Commands[0], "shinyhub deploy ")) {
				t.Fatalf("manual repair missing from recovery: %+v", recovery)
			}
			if tc.command {
				if quarantine.repairCommand != "shinyhub deploy '<source-dir>' --slug app" {
					t.Fatalf("invalid manual deploy syntax: %s", quarantine.repairCommand)
				}
				cmd := newDeployCmd()
				if err := cmd.ParseFlags([]string{"<source-dir>", "--slug", "app"}); err != nil {
					t.Fatal(err)
				}
				if err := cmd.ValidateArgs(cmd.Flags().Args()); err != nil {
					t.Fatalf("manual repair command rejected by deploy CLI: %v", err)
				}
			}
			if tc.name == "older server" && !strings.Contains(recovery.Summary, "manual corrective deployment") {
				t.Fatalf("older-server guidance loops: %+v", recovery)
			}
		})
	}
}

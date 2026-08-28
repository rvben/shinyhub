package cli

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestFleetVerifyPassUsesOnlyTwoGETs(t *testing.T) {
	_, requests := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fleet/health":
			_, _ = w.Write([]byte(`{"complete":true,"apps":{"total":1,"running":1},"replicas":{"running":1}}`))
		case "/api/fleet/schedules/status":
			_, _ = w.Write([]byte(`{"items":[{"slug":"demo","schedule":"producer","enabled":true,"stale":false,"deploy_trigger":"bundle_change","deploy_trigger_satisfied":true,"current_app_version":"v2","current_content_digest":"sha256:new","producer_app_version":"v2","producer_content_digest":"sha256:new"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	out, err := execCLI(t, "fleet", "verify", "-o", "table")
	if err != nil {
		t.Fatalf("fleet verify: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Fleet verification: PASS (read-only)") {
		t.Fatalf("unexpected output:\n%s", out)
	}
	if len(*requests) != 2 {
		t.Fatalf("requests = %d, want exactly two", len(*requests))
	}
	for _, req := range *requests {
		if req.Method != http.MethodGet {
			t.Fatalf("%s %s mutates; fleet verify must be GET-only", req.Method, req.Path)
		}
	}
}

func TestFleetVerifyFailsOnCodeDataMismatch(t *testing.T) {
	_, _ = setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/fleet/health" {
			_, _ = w.Write([]byte(`{"complete":true,"apps":{"total":1,"running":1},"replicas":{"running":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"slug":"demo","schedule":"producer","enabled":true,"stale":false,"deploy_trigger":"bundle_change","deploy_trigger_satisfied":false,"current_app_version":"v2","current_content_digest":"sha256:new","producer_app_version":"v1","producer_content_digest":"sha256:old","convergence_status":"failed"}]}`))
	})
	out, err := execCLI(t, "fleet", "verify", "-o", "table")
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != 4 {
		t.Fatalf("error = %v, want exit 4", err)
	}
	for _, want := range []string{"Fleet verification: FAIL", "code_data_mismatch", "demo/producer", "v2", "v1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

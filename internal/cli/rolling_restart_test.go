package cli

import (
	"net/http"
	"strings"
	"testing"
)

func TestRollingRestartChecksCapabilityAndWaitsForExactActivation(t *testing.T) {
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			w.Write([]byte(`{"version":"dev","capabilities":{"rolling_restart":true}}`))
		case "/api/apps/demo/restart":
			if r.URL.Query().Get("roll") != "true" {
				t.Error("missing rolling request")
			}
			w.WriteHeader(202)
			w.Write([]byte(`{"activation_id":42,"drain_timeout_seconds":60}`))
		case "/api/apps/demo/activations/42":
			w.Write([]byte(`{"status":"succeeded"}`))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	if _, err := execCLI(t, "apps", "restart", "demo", "--roll", "--wait"); err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 3 {
		t.Fatalf("requests: %+v", *reqs)
	}
}

func TestRollingRestartRefusesOlderServerBeforeMutation(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{"version":"dev","capabilities":{}}`)
	_, err := execCLI(t, "apps", "restart", "demo", "--roll")
	if err == nil || !strings.Contains(err.Error(), "does not support rolling") {
		t.Fatalf("error=%v", err)
	}
	for _, r := range *reqs {
		if r.Method != "GET" {
			t.Fatalf("mutated unsupported server: %+v", r)
		}
	}
}

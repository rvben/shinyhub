package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newAppsListServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"slug":"a","status":"running","deploy_count":3},{"slug":"b","status":"stopped","deploy_count":1}]`))
	}))
}

func TestAppsList_JSONEnvelopeWithLimit(t *testing.T) {
	resetFormatState(t)
	srv := newAppsListServer(t)
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")
	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"apps", "list", "--json", "--limit", "1"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("not the standard envelope: %s", out.String())
	}
	if env.Total != 2 || len(env.Items) != 1 {
		t.Errorf("total=%d items=%d", env.Total, len(env.Items))
	}
}

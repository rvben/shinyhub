package api

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestScopedProducerSkipsUIChangeAndRefusesChangedInput(t *testing.T) {
	srv, store, token, rt := newManifestE2EServerWithJobs(t)
	defer srv.Close()
	rt.serveTraffic = true
	srv.cfg.Server.DrainTimeout = 20 * time.Millisecond
	srv.SetAvailableMemoryForTest(func() (int, error) { return 4096, nil })
	app := createGenerationTestApp(t, store, "scoped-producer", 1, 16)
	manifest := `[app]
memory_limit_mb=16
[[schedule]]
name="refresh"
cron="0 5 * * *"
cmd="true"
deploy_trigger="bundle_change"
inputs=["helpers/**"]
`
	upload := func(ui, input string) *httptest.ResponseRecorder {
		body, ct := buildMultiFileBundleUpload(t, map[string]string{"app.py": ui, "helpers/producer.py": input, "shinyhub.toml": manifest})
		req := httptest.NewRequest("POST", "/api/apps/"+app.Slug+"/deploy", body)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}
	if rec := upload("print('v1')", "v1"); rec.Code != 200 {
		t.Fatalf("initial: %d %s", rec.Code, rec.Body.String())
	}
	rt.mu.Lock()
	before := len(rt.producerCommands)
	rt.mu.Unlock()
	if before != 1 {
		t.Fatalf("initial producer runs=%d", before)
	}
	if rec := upload("print('v2')", "v1"); rec.Code != 200 {
		t.Fatalf("UI change: %d %s", rec.Code, rec.Body.String())
	}
	active, _ := store.GetActiveDeploymentGeneration(app.ID)
	if err := srv.waitForPreviousGeneration(t.Context(), app, active.DeploymentID, false); err != nil {
		t.Fatal(err)
	}
	if rec := upload("print('v3')", "v2"); rec.Code != 409 || !strings.Contains(rec.Body.String(), "shared producer state") {
		t.Fatalf("input change: %d %s", rec.Code, rec.Body.String())
	}
	rt.mu.Lock()
	after := len(rt.producerCommands)
	rt.mu.Unlock()
	if after != before {
		t.Fatal("UI change or rejected deploy ran a producer")
	}
	still, _ := store.GetActiveDeploymentGeneration(app.ID)
	if still.DeploymentID != active.DeploymentID {
		t.Fatal("rejected input change replaced working deployment")
	}
}

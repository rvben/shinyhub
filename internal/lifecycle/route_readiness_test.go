package lifecycle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestRecoveredRouteUsesItsDeploymentReadinessContract(t *testing.T) {
	store, app := seedWarmApp(t)
	oldBundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(oldBundle, deploy.ManifestFilename), []byte("[app]\nreadiness_path = '/old-ready'\nreadiness_status = 204\n"), 0600); err != nil {
		t.Fatal(err)
	}
	old, err := store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "old", BundleDir: oldBundle, Status: db.DeploymentSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "new", BundleDir: t.TempDir(), Status: db.DeploymentSucceeded}); err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/old-ready":
			w.WriteHeader(204)
		case "/data":
			w.Write([]byte("old deployment"))
		default:
			w.WriteHeader(503)
		}
	}))
	defer backend.Close()
	p := proxy.New()
	r := &db.Replica{AppID: app.ID, DeploymentID: &old.ID}
	transport := recoveredRouteTransport(p, store, r, nil, backend.URL)
	req, err := http.NewRequestWithContext(context.Background(), "GET", backend.URL+"/data", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("old deployment readiness rejected: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("forwarded status=%d", resp.StatusCode)
	}
}

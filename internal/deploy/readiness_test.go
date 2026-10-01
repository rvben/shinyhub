package deploy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProbeReadinessUsesBundleContract(t *testing.T) {
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, ManifestFilename), []byte("[app]\nreadiness_path = '/ready'\nreadiness_status = 204\n"), 0600); err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			t.Errorf("probe path = %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(204)
	}))
	defer backend.Close()
	if err := ProbeReadiness(context.Background(), backend.URL, bundle, nil); err != nil {
		t.Fatal(err)
	}
}

func TestProbeReadinessRejectsUnhealthyAndBoundsWait(t *testing.T) {
	for _, hung := range []bool{false, true} {
		t.Run(map[bool]string{false: "unhealthy", true: "hung"}[hung], func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if hung {
					<-r.Context().Done()
					return
				}
				w.WriteHeader(503)
			}))
			defer backend.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if err := ProbeReadiness(ctx, backend.URL, t.TempDir(), nil); err == nil {
				t.Fatal("unready endpoint accepted")
			}
		})
	}
}

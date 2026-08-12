package lifecycle

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWaitElasticHealthyUsesReadinessContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health/ready" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	if err := waitElasticHealthy(server.URL, "/health/ready", http.StatusNoContent, time.Second, nil, func() bool { return true }); err != nil {
		t.Fatalf("declared readiness contract failed: %v", err)
	}
	if err := waitElasticHealthy(server.URL, "/", 0, 300*time.Millisecond, nil, func() bool { return true }); err == nil {
		t.Fatal("default readiness accepted a 404")
	}
}

package process

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestDockerClient wires a dockerClient to a httptest.Server using TCP (not Unix socket).
func newTestDockerClient(srv *httptest.Server) *dockerClient {
	return &dockerClient{base: srv.URL, hc: srv.Client(), stream: srv.Client()}
}

func TestDockerClientCreateAndStart(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/create"):
			json.NewDecoder(r.Body).Decode(&gotBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"Id": "abc123"})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/start"):
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestDockerClient(srv)
	id, err := c.createContainer(containerConfig{
		Image:   "alpine",
		Cmd:     []string{"sleep", "10"},
		WorkDir: "/app",
	})
	if err != nil {
		t.Fatalf("createContainer: %v", err)
	}
	if id != "abc123" {
		t.Errorf("expected id abc123, got %s", id)
	}

	if err := c.startContainer(id); err != nil {
		t.Fatalf("startContainer: %v", err)
	}
}

func TestDockerClientInspect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/json") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"State": map[string]any{"Running": true, "Pid": 1234},
			})
		}
	}))
	defer srv.Close()

	c := newTestDockerClient(srv)
	state, err := c.inspectContainer("abc123")
	if err != nil {
		t.Fatalf("inspectContainer: %v", err)
	}
	if !state.Running {
		t.Error("expected Running=true")
	}
	if state.Pid != 1234 {
		t.Errorf("expected Pid=1234, got %d", state.Pid)
	}
}


func TestDockerClientContainerStatsFormula(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/stats") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"cpu_stats": map[string]any{
					"cpu_usage":        map[string]any{"total_usage": uint64(200_000_000)},
					"system_cpu_usage": uint64(1_000_000_000),
					"online_cpus":      2,
				},
				"precpu_stats": map[string]any{
					"cpu_usage":        map[string]any{"total_usage": uint64(100_000_000)},
					"system_cpu_usage": uint64(900_000_000),
				},
				"memory_stats": map[string]any{
					"usage": uint64(50 * 1024 * 1024),
					"cache": uint64(10 * 1024 * 1024),
				},
			})
		}
	}))
	defer srv.Close()

	c := newTestDockerClient(srv)
	cpu, rss, err := c.containerStats(context.Background(), "abc")
	if err != nil {
		t.Fatalf("containerStats: %v", err)
	}
	// cpuDelta=100M, systemDelta=100M, numCPU=2 → (100M/100M)*2*100 = 200%
	if cpu != 200.0 {
		t.Errorf("expected cpu=200.0, got %f", cpu)
	}
	// rss = 50MB - 10MB cache = 40MB
	if rss != 40*1024*1024 {
		t.Errorf("expected rss=40MB, got %d", rss)
	}
}

func TestDockerClientListByLabel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/containers/json" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]map[string]any{
				{"Id": "cid1", "Labels": map[string]string{"shinyhub.slug": "my-app"}, "State": "running"},
			})
		}
	}))
	defer srv.Close()

	c := newTestDockerClient(srv)
	containers, err := c.listContainers(fmt.Sprintf(`{"label":[%q]}`, "shinyhub.managed=true"))
	if err != nil {
		t.Fatalf("listContainers: %v", err)
	}
	if len(containers) != 1 || containers[0].ID != "cid1" {
		t.Errorf("unexpected containers: %v", containers)
	}
}

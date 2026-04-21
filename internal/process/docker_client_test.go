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

// TestDockerClientCreateContainer_PortBindingsJSONShape verifies the wire
// shape Docker requires for port publishing: ExposedPorts at the top level
// AND HostConfig.PortBindings, both keyed by "<port>/tcp", with a single
// {HostIp, HostPort} entry per container port. Mistakes in this shape are
// silently ignored by the daemon, which is why we lock it down explicitly.
func TestDockerClientCreateContainer_PortBindingsJSONShape(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/create") {
			json.NewDecoder(r.Body).Decode(&captured)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"Id": "p1"})
		}
	}))
	defer srv.Close()

	c := newTestDockerClient(srv)
	if _, err := c.createContainer(containerConfig{
		Image:       "alpine",
		WorkDir:     "/app",
		NetworkMode: "bridge",
		Ports: []containerPortBinding{
			{ContainerPort: 31415, HostPort: 31415, HostIP: "127.0.0.1"},
		},
	}); err != nil {
		t.Fatalf("createContainer: %v", err)
	}

	exposed, ok := captured["ExposedPorts"].(map[string]any)
	if !ok {
		t.Fatalf("expected top-level ExposedPorts map, got %v", captured["ExposedPorts"])
	}
	if _, ok := exposed["31415/tcp"]; !ok {
		t.Errorf("ExposedPorts must contain 31415/tcp, got %v", exposed)
	}

	host, ok := captured["HostConfig"].(map[string]any)
	if !ok {
		t.Fatalf("expected HostConfig, got %v", captured["HostConfig"])
	}
	bindings, ok := host["PortBindings"].(map[string]any)
	if !ok {
		t.Fatalf("expected HostConfig.PortBindings map, got %v", host["PortBindings"])
	}
	entry, ok := bindings["31415/tcp"].([]any)
	if !ok || len(entry) != 1 {
		t.Fatalf("expected single PortBindings[31415/tcp] entry, got %v", bindings["31415/tcp"])
	}
	first, _ := entry[0].(map[string]any)
	if first["HostIp"] != "127.0.0.1" {
		t.Errorf("HostIp = %v, want 127.0.0.1", first["HostIp"])
	}
	if first["HostPort"] != "31415" {
		t.Errorf("HostPort = %v, want \"31415\"", first["HostPort"])
	}
}

// TestDockerClientCreateContainer_NoPortsOmitsBindings verifies that omitting
// Ports entirely produces a body with neither ExposedPorts nor PortBindings —
// host-network mode containers must not carry vestigial port-binding fields.
func TestDockerClientCreateContainer_NoPortsOmitsBindings(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/create") {
			json.NewDecoder(r.Body).Decode(&captured)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"Id": "p2"})
		}
	}))
	defer srv.Close()

	c := newTestDockerClient(srv)
	if _, err := c.createContainer(containerConfig{
		Image:       "alpine",
		WorkDir:     "/app",
		NetworkMode: "host",
	}); err != nil {
		t.Fatalf("createContainer: %v", err)
	}

	if _, ok := captured["ExposedPorts"]; ok {
		t.Errorf("ExposedPorts must be absent when Ports is empty, got %v", captured["ExposedPorts"])
	}
	host, _ := captured["HostConfig"].(map[string]any)
	if _, ok := host["PortBindings"]; ok {
		t.Errorf("HostConfig.PortBindings must be absent when Ports is empty, got %v", host["PortBindings"])
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

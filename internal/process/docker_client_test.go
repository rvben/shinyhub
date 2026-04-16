package process

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestDockerClient wires a dockerClient to a httptest.Server using TCP (not Unix socket).
func newTestDockerClient(srv *httptest.Server) *dockerClient {
	return &dockerClient{base: srv.URL, hc: srv.Client()}
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

func TestDockerClientStop(t *testing.T) {
	stopped := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/stop") {
			stopped = true
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	c := newTestDockerClient(srv)
	if err := c.stopContainer("abc123", 5); err != nil {
		t.Fatalf("stopContainer: %v", err)
	}
	if !stopped {
		t.Error("expected stop request to be sent")
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

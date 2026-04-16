package process

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
)

func newDockerRuntimeWithServer(t *testing.T, handler http.Handler) *DockerRuntime {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := &dockerClient{base: srv.URL, hc: srv.Client()}
	return &DockerRuntime{
		client:      client,
		pythonImage: "uv-test:latest",
		rImage:      "r-test:latest",
	}
}

func TestDockerRuntimeStart(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/containers/create", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"Id": "cont1"})
	})
	mux.HandleFunc("/containers/cont1/start", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	rt := newDockerRuntimeWithServer(t, mux)
	handle, err := rt.Start(context.Background(), StartParams{
		Slug:    "my-app",
		Dir:     t.TempDir(),
		Command: []string{"uv", "run", "shiny", "run", "app.py"},
		Port:    20001,
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if handle.ContainerID != "cont1" {
		t.Errorf("expected cont1, got %s", handle.ContainerID)
	}
}

func TestDockerRuntimeSignal(t *testing.T) {
	killed := false
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/kill") {
			killed = true
			w.WriteHeader(http.StatusNoContent)
		}
	})

	rt := newDockerRuntimeWithServer(t, mux)
	if err := rt.Signal(RunHandle{ContainerID: "cont1"}, syscall.SIGTERM); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	if !killed {
		t.Error("expected kill request")
	}
}

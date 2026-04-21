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
	client := &dockerClient{base: srv.URL, hc: srv.Client(), stream: srv.Client()}
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

func TestDockerRuntimeStart_AppDataPathBindsTwiceAndOverridesEnv(t *testing.T) {
	var captured map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/containers/create", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		captured = body
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"Id": "cont-data"})
	})
	mux.HandleFunc("/containers/cont-data/start", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	rt := newDockerRuntimeWithServer(t, mux)
	hostData := "/var/lib/shinyhub/data/demo"

	_, err := rt.Start(context.Background(), StartParams{
		Slug:        "demo",
		Dir:         t.TempDir(),
		Command:     []string{"uv", "run", "shiny", "run", "app.py"},
		Port:        9999,
		AppDataPath: hostData,
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	host, ok := captured["HostConfig"].(map[string]any)
	if !ok {
		t.Fatalf("no HostConfig in body: %v", captured)
	}
	mounts, ok := host["Mounts"].([]any)
	if !ok {
		t.Fatalf("no Mounts in HostConfig: %v", host)
	}

	hasMount := func(target string) bool {
		for _, m := range mounts {
			mm, _ := m.(map[string]any)
			if mm["Source"] == hostData && mm["Target"] == target {
				return true
			}
		}
		return false
	}
	if !hasMount("/app-data") {
		t.Errorf("missing /app-data mount in: %v", mounts)
	}
	if !hasMount("/app/data") {
		t.Errorf("missing /app/data mount in: %v", mounts)
	}

	envRaw, _ := captured["Env"].([]any)
	var lastAppData string
	for _, e := range envRaw {
		s, _ := e.(string)
		if strings.HasPrefix(s, "SHINYHUB_APP_DATA=") {
			lastAppData = strings.TrimPrefix(s, "SHINYHUB_APP_DATA=")
		}
	}
	if lastAppData != "/app-data" {
		t.Errorf("SHINYHUB_APP_DATA last value = %q, want /app-data", lastAppData)
	}
}

func TestDockerRuntimeStart_NoAppDataPathSkipsDataMounts(t *testing.T) {
	var captured map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/containers/create", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		captured = body
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"Id": "cont-nodata"})
	})
	mux.HandleFunc("/containers/cont-nodata/start", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	rt := newDockerRuntimeWithServer(t, mux)
	bundleDir := t.TempDir()

	_, err := rt.Start(context.Background(), StartParams{
		Slug:    "no-data",
		Dir:     bundleDir,
		Command: []string{"uv", "run", "shiny", "run", "app.py"},
		Port:    9999,
		// AppDataPath intentionally empty.
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	host, _ := captured["HostConfig"].(map[string]any)
	mounts, _ := host["Mounts"].([]any)
	for _, m := range mounts {
		mm, _ := m.(map[string]any)
		if mm["Target"] == "/app-data" || mm["Target"] == "/app/data" {
			t.Errorf("should not have data mount when AppDataPath is empty, got %v", mm)
		}
	}
}

func TestDockerRuntimeImageForCommand(t *testing.T) {
	rt := &DockerRuntime{pythonImage: "uv:latest", rImage: "r-base:latest"}

	tests := []struct {
		cmd  []string
		want string
	}{
		{[]string{"uv", "run", "app.py"}, "uv:latest"},
		{[]string{"Rscript", "app.R"}, "r-base:latest"},
		{[]string{"/usr/bin/Rscript", "app.R"}, "r-base:latest"},
		{[]string{}, "uv:latest"}, // empty → python default
	}

	for _, tc := range tests {
		got := rt.imageForCommand(tc.cmd)
		if got != tc.want {
			t.Errorf("imageForCommand(%v) = %q, want %q", tc.cmd, got, tc.want)
		}
	}
}

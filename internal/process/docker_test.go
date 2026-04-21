package process

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const dockerSocketPath = "/var/run/docker.sock"

// dockerRuntimeWithImage returns a real DockerRuntime with imageRef ready to
// run. It transparently pulls imageRef when missing so CI runners do not need
// to pre-seed an image cache. The test is skipped (not failed) if either the
// daemon or the registry cannot satisfy the precondition; image-cache state
// and network reachability are environmental, not behaviour the code under
// test is responsible for.
func dockerRuntimeWithImage(t *testing.T, imageRef string) *DockerRuntime {
	t.Helper()
	// Use "host" network so the integration tests don't need TCP port
	// publishing on the test daemon's loopback (which would race with parallel
	// runs that all want the same host port).
	rt, err := NewDockerRuntime(dockerSocketPath, imageRef, imageRef, "host")
	if err != nil {
		t.Skipf("Docker daemon unavailable: %v", err)
	}
	ensureDockerImage(t, rt.client, imageRef)
	return rt
}

// ensureDockerImage makes imageRef present in the daemon's local cache, pulling
// it on demand. Callers should already hold a working *dockerClient. The test
// is skipped on any environmental failure (no network, registry rate-limit,
// daemon refusal) — failing would mask cache state as a code defect.
func ensureDockerImage(t *testing.T, c *dockerClient, imageRef string) {
	t.Helper()
	inspectPath := "/images/" + url.PathEscape(imageRef) + "/json"
	if err := c.get(inspectPath, nil); err == nil {
		return
	}

	repo, tag, ok := strings.Cut(imageRef, ":")
	if !ok {
		tag = "latest"
	}
	pullURL := c.base + "/images/create?fromImage=" + url.QueryEscape(repo) + "&tag=" + url.QueryEscape(tag)
	req, err := http.NewRequest(http.MethodPost, pullURL, nil)
	if err != nil {
		t.Skipf("ensure %s: build pull request: %v", imageRef, err)
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		t.Skipf("ensure %s: pull dispatch: %v", imageRef, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		t.Skipf("ensure %s: daemon returned %d: %s", imageRef, resp.StatusCode, body)
	}
	// The Docker pull API streams JSON progress lines; the actual download
	// only completes once the response body is fully drained.
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Skipf("ensure %s: pull stream interrupted: %v", imageRef, err)
	}
	if err := c.get(inspectPath, nil); err != nil {
		t.Skipf("ensure %s: image still missing after pull: %v", imageRef, err)
	}
}

func newDockerRuntimeWithServer(t *testing.T, handler http.Handler) *DockerRuntime {
	t.Helper()
	return newDockerRuntimeWithServerAndMode(t, handler, "host")
}

func newDockerRuntimeWithServerAndMode(t *testing.T, handler http.Handler, networkMode string) *DockerRuntime {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := &dockerClient{base: srv.URL, hc: srv.Client(), stream: srv.Client()}
	return &DockerRuntime{
		client:      client,
		pythonImage: "uv-test:latest",
		rImage:      "r-test:latest",
		networkMode: networkMode,
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

// TestAddSharedMounts_PreCreatesMountTargetHostSide locks in the host-side
// pre-creation that prevents the Docker daemon from materializing the bind-
// mount target with root ownership (which would leave the workspace with
// undeletable directories).
func TestAddSharedMounts_PreCreatesMountTargetHostSide(t *testing.T) {
	workspace := t.TempDir()
	sourceData := t.TempDir()
	cfg := &containerConfig{WorkDir: "/app"}

	err := addSharedMounts(cfg,
		[]SharedMount{{SourceSlug: "fetch", HostPath: sourceData}},
		filepath.Join(workspace, "data"),
	)
	if err != nil {
		t.Fatalf("addSharedMounts: %v", err)
	}

	target := filepath.Join(workspace, "data", "shared", "fetch")
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected target %s to be a directory", target)
	}
}

// TestDockerRuntime_AppBindHost_ByMode locks in the per-mode bind address:
// host-network mode keeps the loopback boundary; bridge mode must bind
// 0.0.0.0 so the published 127.0.0.1:port mapping actually routes.
func TestDockerRuntime_AppBindHost_ByMode(t *testing.T) {
	tests := []struct {
		mode string
		want string
	}{
		{"host", "127.0.0.1"},
		{"bridge", "0.0.0.0"},
		{"", "0.0.0.0"}, // unspecified collapses to bridge semantics
	}
	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			rt := &DockerRuntime{networkMode: tc.mode}
			if got := rt.AppBindHost(); got != tc.want {
				t.Errorf("AppBindHost(mode=%q) = %q, want %q", tc.mode, got, tc.want)
			}
		})
	}
}

// TestDockerRuntime_Start_BridgeModePublishesPortToLoopback verifies that
// bridge-network containers ship their listening port back to 127.0.0.1:port
// via PortBindings — the missing piece that would otherwise leave bridge
// containers unreachable from the in-process proxy.
func TestDockerRuntime_Start_BridgeModePublishesPortToLoopback(t *testing.T) {
	var captured map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/containers/create", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"Id": "cont-bridge"})
	})
	mux.HandleFunc("/containers/cont-bridge/start", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	rt := newDockerRuntimeWithServerAndMode(t, mux, "bridge")
	if _, err := rt.Start(context.Background(), StartParams{
		Slug:    "br",
		Dir:     t.TempDir(),
		Command: []string{"true"},
		Port:    20123,
	}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	host, _ := captured["HostConfig"].(map[string]any)
	if got := host["NetworkMode"]; got != "bridge" {
		t.Errorf("HostConfig.NetworkMode = %v, want bridge", got)
	}
	bindings, _ := host["PortBindings"].(map[string]any)
	entry, ok := bindings["20123/tcp"].([]any)
	if !ok || len(entry) == 0 {
		t.Fatalf("expected PortBindings[20123/tcp] entry, got %v", bindings)
	}
	first, _ := entry[0].(map[string]any)
	if first["HostIp"] != "127.0.0.1" {
		t.Errorf("HostIp = %v, want 127.0.0.1", first["HostIp"])
	}
	if first["HostPort"] != "20123" {
		t.Errorf("HostPort = %v, want \"20123\"", first["HostPort"])
	}
	exposed, _ := captured["ExposedPorts"].(map[string]any)
	if _, ok := exposed["20123/tcp"]; !ok {
		t.Errorf("expected ExposedPorts to declare 20123/tcp, got %v", exposed)
	}
}

// TestDockerRuntime_Start_HostModeOmitsPortBindings verifies that host-network
// mode does NOT publish ports — the container is already on the host network
// stack, so adding PortBindings would be redundant and confusing in `docker ps`.
func TestDockerRuntime_Start_HostModeOmitsPortBindings(t *testing.T) {
	var captured map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/containers/create", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"Id": "cont-host"})
	})
	mux.HandleFunc("/containers/cont-host/start", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	rt := newDockerRuntimeWithServerAndMode(t, mux, "host")
	if _, err := rt.Start(context.Background(), StartParams{
		Slug:    "hm",
		Dir:     t.TempDir(),
		Command: []string{"true"},
		Port:    20999,
	}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	host, _ := captured["HostConfig"].(map[string]any)
	if got := host["NetworkMode"]; got != "host" {
		t.Errorf("HostConfig.NetworkMode = %v, want host", got)
	}
	if _, ok := host["PortBindings"]; ok {
		t.Errorf("HostConfig.PortBindings must be absent in host mode, got %v", host["PortBindings"])
	}
	if _, ok := captured["ExposedPorts"]; ok {
		t.Errorf("ExposedPorts must be absent in host mode, got %v", captured["ExposedPorts"])
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

func TestDockerRuntime_RunOnce_ExitsCleanly(t *testing.T) {
	rt := dockerRuntimeWithImage(t, "alpine:3")
	var buf bytes.Buffer
	p := StartParams{
		Slug: "x", Dir: t.TempDir(),
		Command: []string{"sh", "-c", "echo hello; exit 5"},
	}
	info, err := rt.RunOnce(context.Background(), p, &buf)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if info.Code != 5 {
		t.Fatalf("expected exit 5, got %d", info.Code)
	}
}

func TestDockerRuntime_RunOnce_SharedMountIsReadOnly(t *testing.T) {
	rt := dockerRuntimeWithImage(t, "alpine:3")
	sourceData := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceData, "marker"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	var buf bytes.Buffer
	p := StartParams{
		Slug: "consumer", Dir: t.TempDir(),
		Command:      []string{"sh", "-c", "echo hi > /app/data/shared/fetch/should-fail"},
		SharedMounts: []SharedMount{{SourceSlug: "fetch", HostPath: sourceData}},
	}
	info, err := rt.RunOnce(context.Background(), p, &buf)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if info.Code == 0 {
		t.Fatalf("expected nonzero exit (write to RO mount), got 0; output=%q", buf.String())
	}
}

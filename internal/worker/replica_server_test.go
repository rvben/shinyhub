package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/worker/api"
)

// fakeRuntime records Start params and returns a canned endpoint, letting the
// replica server be tested without Docker.
type fakeRuntime struct {
	startParams process.StartParams
	startURL    string
	startErr    error
}

func (f *fakeRuntime) Start(_ context.Context, p process.StartParams, w io.Writer) (process.ReplicaEndpoint, error) {
	f.startParams = p
	if f.startErr != nil {
		return process.ReplicaEndpoint{}, f.startErr
	}
	_, _ = io.WriteString(w, "starting\n")
	url := f.startURL
	if url == "" {
		url = "http://127.0.0.1:8080"
	}
	return process.ReplicaEndpoint{URL: url, Provider: "docker", Handle: process.RunHandle{ContainerID: "c-1"}}, nil
}
func (f *fakeRuntime) Signal(process.RunHandle, syscall.Signal) error { return nil }
func (f *fakeRuntime) Wait(context.Context, process.RunHandle) error  { return nil }
func (f *fakeRuntime) Stats(context.Context, process.RunHandle) (float64, uint64, error) {
	return 1.5, 2048, nil
}
func (f *fakeRuntime) RunOnce(_ context.Context, p process.StartParams, w io.Writer) (process.ExitInfo, error) {
	f.startParams = p
	_, _ = io.WriteString(w, "job ran\n")
	return process.ExitInfo{Code: 0}, nil
}
func (f *fakeRuntime) HostPreparesDeps() bool    { return false }
func (f *fakeRuntime) AppBindHost() string       { return "0.0.0.0" }
func (f *fakeRuntime) HostProvidesAppData() bool { return true }

func TestNewReplicaServer_AllocatesPortAndProvisionsAppData(t *testing.T) {
	dir := t.TempDir()
	rt := &fakeRuntime{}
	srv := NewReplicaServer(ReplicaServerConfig{
		Runtime:      rt,
		DataDir:      dir,
		NodeID:       "node-a",
		Advertise:    "worker.example:8443",
		AllocatePort: func() int { return 49001 },
	})

	// app-data root must resolve under the data dir.
	appData, err := srv.provisionAppData("app")
	if err != nil {
		t.Fatalf("provisionAppData: %v", err)
	}
	want := filepath.Join(dir, "app-data", "app")
	if appData != want {
		t.Errorf("provisionAppData = %q, want %q", appData, want)
	}

	port := srv.allocatePort()
	if port != 49001 {
		t.Errorf("allocatePort = %d, want 49001", port)
	}
}

func TestReplicaServer_StartStreamsResultThenLogs(t *testing.T) {
	dir := t.TempDir()
	rt := &fakeRuntime{startURL: "http://127.0.0.1:49001"}
	srv := NewReplicaServer(ReplicaServerConfig{
		Runtime:      rt,
		DataDir:      dir,
		NodeID:       "node-a",
		Advertise:    "worker.example:8443",
		AllocatePort: func() int { return 49001 },
	})

	body, _ := json.Marshal(api.ReplicaStartRequest{
		Slug: "app", Index: 0, Tier: "remote",
		Command: []string{"./server"}, BindPort: 8080,
		SharedMountSlugs: []string{"shared"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/replicas", bytes.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() { srv.handleStart(rec, req); close(done) }()
	// Give the handler time to write the result and log frames, then cancel so
	// the blocking handler returns.
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	dec := json.NewDecoder(rec.Body)
	var result api.ReplicaResult
	sawResult, sawLog := false, false
	for {
		var fr api.Frame
		if err := dec.Decode(&fr); err != nil {
			break
		}
		switch fr.Kind {
		case api.FrameResult:
			if err := json.Unmarshal(fr.Data, &result); err != nil {
				t.Fatalf("decode result: %v", err)
			}
			sawResult = true
		case api.FrameLog:
			sawLog = true
		}
	}
	if !sawResult {
		t.Fatal("no result frame received")
	}
	if !sawLog {
		t.Error("expected at least one log frame")
	}
	if result.NodeID != "node-a" {
		t.Errorf("result NodeID = %q, want node-a", result.NodeID)
	}
	if !strings.HasPrefix(result.URL, "https://worker.example:8443/v1/data/") {
		t.Errorf("result URL = %q, want tunnel URL", result.URL)
	}

	// Bind port is preserved into the container; host publish port is allocated.
	if rt.startParams.Port != 8080 {
		t.Errorf("bind Port = %d, want 8080", rt.startParams.Port)
	}
	if rt.startParams.HostPublishPort != 49001 {
		t.Errorf("HostPublishPort = %d, want 49001", rt.startParams.HostPublishPort)
	}
	// app-data provisioned locally on the worker; shared mount resolved to a
	// worker-local path.
	if rt.startParams.AppDataPath == "" {
		t.Error("AppDataPath empty: worker must provision app-data locally")
	}
	if len(rt.startParams.SharedMounts) != 1 || rt.startParams.SharedMounts[0].HostPath == "" {
		t.Errorf("shared mount not resolved to worker-local path: %+v", rt.startParams.SharedMounts)
	}
}

func withURLParam(r *http.Request, key, val string) *http.Request {
	rctx, ok := r.Context().Value(chi.RouteCtxKey).(*chi.Context)
	if !ok {
		rctx = chi.NewRouteContext()
	}
	rctx.URLParams.Add(key, val)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestReplicaServer_SignalWaitStats(t *testing.T) {
	dir := t.TempDir()
	rt := &fakeRuntime{}
	srv := NewReplicaServer(ReplicaServerConfig{
		Runtime: rt, DataDir: dir, NodeID: "node-a", Advertise: "w:8443",
		AllocatePort: func() int { return 49001 },
	})
	// Seed a record as if a replica were started.
	srv.mu.Lock()
	srv.byContainer["c-1"] = &replicaRecord{token: "tok", containerID: "c-1", handle: process.RunHandle{ContainerID: "c-1"}, hostPort: 49001}
	srv.mu.Unlock()

	// Signal
	sb, _ := json.Marshal(api.SignalRequest{Signal: int(syscall.SIGTERM)})
	req := httptest.NewRequest(http.MethodPost, "/v1/replicas/c-1/signal", bytes.NewReader(sb))
	req = withURLParam(req, "container", "c-1")
	rec := httptest.NewRecorder()
	srv.handleSignal(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("signal status = %d", rec.Code)
	}

	// Stats
	req = httptest.NewRequest(http.MethodGet, "/v1/replicas/c-1/stats", nil)
	req = withURLParam(req, "container", "c-1")
	rec = httptest.NewRecorder()
	srv.handleStats(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("stats status = %d", rec.Code)
	}
	var stats api.StatsResult
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.CPUPercent != 1.5 || stats.RSSBytes != 2048 {
		t.Errorf("stats = %+v, want {CPUPercent:1.5 RSSBytes:2048}", stats)
	}

	// Unknown container is rejected.
	req = httptest.NewRequest(http.MethodGet, "/v1/replicas/nope/stats", nil)
	req = withURLParam(req, "container", "nope")
	rec = httptest.NewRecorder()
	srv.handleStats(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown container stats status = %d, want 404", rec.Code)
	}

	// Wait: seed both tables, then wait removes the replica from both.
	srv.mu.Lock()
	srv.byContainer["c-2"] = &replicaRecord{token: "tok2", containerID: "c-2", handle: process.RunHandle{ContainerID: "c-2"}, hostPort: 49002}
	srv.byToken["tok2"] = srv.byContainer["c-2"]
	srv.mu.Unlock()

	req = httptest.NewRequest(http.MethodPost, "/v1/replicas/c-2/wait", nil)
	req = withURLParam(req, "container", "c-2")
	rec = httptest.NewRecorder()
	srv.handleWait(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("wait status = %d, want 204", rec.Code)
	}
	srv.mu.RLock()
	_, okC := srv.byContainer["c-2"]
	_, okT := srv.byToken["tok2"]
	srv.mu.RUnlock()
	if okC || okT {
		t.Errorf("wait did not remove replica from both tables: byContainer=%v byToken=%v", okC, okT)
	}
}

func TestReplicaServer_RebuildFromContainers(t *testing.T) {
	dir := t.TempDir()
	rt := &fakeLister{
		containers: []process.ContainerInfo{
			{ID: "c-1", Labels: map[string]string{
				"shinyhub.slug": "app", "shinyhub.replica_index": "0",
			}},
			// A one-shot job container (no replica_index) must be ignored.
			{ID: "job-1", Labels: map[string]string{"shinyhub.slug": "app"}},
		},
		hostPorts: map[string]int{"c-1": 49001},
	}
	srv := NewReplicaServer(ReplicaServerConfig{
		Runtime: rt, DataDir: dir, NodeID: "node-a", Advertise: "w:8443",
		AllocatePort: func() int { return 49001 },
	})

	if err := srv.RebuildFromContainers(); err != nil {
		t.Fatalf("RebuildFromContainers: %v", err)
	}

	srv.mu.RLock()
	defer srv.mu.RUnlock()
	rec, ok := srv.byContainer["c-1"]
	if !ok {
		t.Fatal("replica container c-1 not re-adopted")
	}
	if rec.token == "" {
		t.Error("re-adopted container has no token")
	}
	if rec.hostPort != 49001 {
		t.Errorf("re-adopted hostPort = %d, want 49001", rec.hostPort)
	}
	if srv.byToken[rec.token] != rec {
		t.Error("byToken not rebuilt consistently with byContainer")
	}
	if _, ok := srv.byContainer["job-1"]; ok {
		t.Error("one-shot job container should not be re-adopted as a replica")
	}
}

func TestReplicaServer_DataPlaneUnresolvedPortReturns503(t *testing.T) {
	dir := t.TempDir()
	srv := NewReplicaServer(ReplicaServerConfig{
		Runtime: &fakeRuntime{}, DataDir: dir, NodeID: "node-a", Advertise: "w:8443",
		AllocatePort: func() int { return 0 },
	})
	srv.mu.Lock()
	srv.byToken["tok"] = &replicaRecord{token: "tok", containerID: "c-1"} // hostPort 0
	srv.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/v1/data/tok/health", nil)
	req = withURLParam(req, "token", "tok")
	req = withURLParam(req, "*", "health")
	rec := httptest.NewRecorder()
	srv.handleData(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestReplicaServer_DataPlaneProxiesToHostPort(t *testing.T) {
	// Backend stands in for the published container port on the worker.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "path="+r.URL.Path)
	}))
	defer backend.Close()

	// Parse the backend port so the record points at it.
	u, _ := url.Parse(backend.URL)
	hostPort, _ := strconv.Atoi(u.Port())

	dir := t.TempDir()
	srv := NewReplicaServer(ReplicaServerConfig{
		Runtime: &fakeRuntime{}, DataDir: dir, NodeID: "node-a", Advertise: "w:8443",
		AllocatePort: func() int { return hostPort },
	})
	srv.mu.Lock()
	srv.byToken["tok"] = &replicaRecord{token: "tok", containerID: "c-1", hostPort: hostPort}
	srv.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/v1/data/tok/health/ready", nil)
	req = withURLParam(req, "token", "tok")
	req = withURLParam(req, "*", "health/ready")
	rec := httptest.NewRecorder()
	srv.handleData(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// The /v1/data/{token} prefix must be stripped before reaching the backend.
	if got := rec.Body.String(); got != "path=/health/ready" {
		t.Errorf("backend saw %q, want path=/health/ready", got)
	}
}

// fakeEnsurer implements BundleEnsurer for tests. It records the digest it was
// called with and returns a fixed directory path.
type fakeEnsurer struct {
	called bool
	digest string
	dir    string
}

func (f *fakeEnsurer) Ensure(_ context.Context, digest string) (string, error) {
	f.called = true
	f.digest = digest
	return f.dir, nil
}

// spyFlusher counts Flush calls so tests can assert a closed writer never
// touches the (by then finalized) response.
type spyFlusher struct{ flushes int }

func (s *spyFlusher) Flush() { s.flushes++ }

// TestFrameLogWriter_InertAfterClose pins the contract that makes the worker
// crash-safe: the runtime spawns a detached goroutine that keeps writing
// container logs into the frame writer, but the request handler returns (and
// the response is finalized) as soon as the user disconnects. Writing to the
// finalized ResponseWriter then panics and kills the worker. After close() the
// writer must be inert - Write returns an error and touches neither the encoder
// nor the flusher.
func TestFrameLogWriter_InertAfterClose(t *testing.T) {
	var buf bytes.Buffer
	fl := &spyFlusher{}
	w := &frameLogWriter{enc: json.NewEncoder(&buf), flusher: fl}

	n, err := w.Write([]byte("hello"))
	if err != nil || n != len("hello") {
		t.Fatalf("pre-close Write = (%d, %v), want (%d, nil)", n, err, len("hello"))
	}
	if fl.flushes != 1 {
		t.Fatalf("pre-close flushes = %d, want 1", fl.flushes)
	}

	w.close()

	encodedLen := buf.Len()
	if _, err := w.Write([]byte("late log from a detached streamLogs goroutine")); err == nil {
		t.Error("post-close Write returned nil error, want a closed error")
	}
	if buf.Len() != encodedLen {
		t.Error("post-close Write encoded a frame; writer must be inert after close")
	}
	if fl.flushes != 1 {
		t.Errorf("post-close flushes = %d, want 1 (close must stop flushing)", fl.flushes)
	}
}

// TestFrameLogWriter_ConcurrentWriteAndCloseRaceSafe runs writes concurrently
// with close(), mirroring the handler returning while the runtime's detached
// log goroutine is still writing. It must be race-free under -race.
func TestFrameLogWriter_ConcurrentWriteAndCloseRaceSafe(t *testing.T) {
	var buf bytes.Buffer
	w := &frameLogWriter{enc: json.NewEncoder(&buf), flusher: &spyFlusher{}}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_, _ = w.Write([]byte("log line from the streaming goroutine"))
		}
	}()
	w.close()
	wg.Wait()
}

func TestBuildStartParams_BundleCacheWired(t *testing.T) {
	ensureDir := t.TempDir()
	fe := &fakeEnsurer{dir: ensureDir}

	srv := NewReplicaServer(ReplicaServerConfig{
		Runtime:      &fakeRuntime{},
		DataDir:      t.TempDir(),
		NodeID:       "node-a",
		Advertise:    "w:8443",
		AllocatePort: func() int { return 49001 },
		Bundles:      fe,
	})

	// Happy path: content digest present, ensurer called, params.Dir set.
	params, err := srv.buildStartParams(context.Background(), api.ReplicaStartRequest{
		Slug: "app", Index: 0, BindPort: 8000, ContentDigest: "sha256:deadbeef",
	}, 49001)
	if err != nil {
		t.Fatalf("buildStartParams: %v", err)
	}
	if !fe.called {
		t.Error("BundleEnsurer.Ensure was not called")
	}
	if fe.digest != "sha256:deadbeef" {
		t.Errorf("Ensure called with digest %q, want sha256:deadbeef", fe.digest)
	}
	if params.Dir != ensureDir {
		t.Errorf("params.Dir = %q, want %q", params.Dir, ensureDir)
	}

	// Error path: Bundles wired but ContentDigest empty returns an error.
	_, err = srv.buildStartParams(context.Background(), api.ReplicaStartRequest{
		Slug: "app", Index: 0, BindPort: 8000, ContentDigest: "",
	}, 49001)
	if err == nil {
		t.Error("expected error when ContentDigest is empty with Bundles set, got nil")
	}
}

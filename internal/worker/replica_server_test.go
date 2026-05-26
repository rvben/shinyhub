package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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

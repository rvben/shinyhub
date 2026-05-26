package worker

import (
	"context"
	"io"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/rvben/shinyhub/internal/process"
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

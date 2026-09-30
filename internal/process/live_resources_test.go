package process

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"syscall"
	"testing"
)

func TestDockerLiveResourcesPreservesSiblingLimitAndSwapAllowance(t *testing.T) {
	var payload map[string]int64
	rt := newDockerRuntimeWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/containers/live/json":
			_, _ = io.WriteString(w, `{"HostConfig":{"Memory":67108864,"MemorySwap":134217728}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/containers/live/update":
			payload = make(map[string]int64)
			_ = json.NewDecoder(r.Body).Decode(&payload)
			_, _ = io.WriteString(w, `{"Warnings":[]}`)
		default:
			t.Errorf("unexpected mutation: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	memory := 128
	if err := rt.UpdateResources(context.Background(), RunHandle{ContainerID: "live"}, ResourceLimits{MemoryLimitMB: &memory}); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 2 || payload["Memory"] != 128*1024*1024 || payload["MemorySwap"] != 192*1024*1024 {
		t.Fatalf("CPU or swap allowance changed: %v", payload)
	}
	cpu := 150
	if err := rt.UpdateResources(context.Background(), RunHandle{ContainerID: "live"}, ResourceLimits{CPUQuotaPercent: &cpu}); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 1 || payload["NanoCpus"] != 1_500_000_000 {
		t.Fatalf("memory changed with CPU: %v", payload)
	}
}

func TestDockerLiveResourceUpdateKeepsRealContainerRunning(t *testing.T) {
	rt := dockerRuntimeWithImage(t, "alpine:3")
	ep, err := rt.Start(context.Background(), StartParams{Slug: "live-settings-test", Dir: t.TempDir(), Command: []string{"sleep", "60"}, MemoryLimitMB: 64, CPUQuotaPercent: 100}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Signal(ep.Handle, syscall.SIGKILL); _ = rt.RemoveHandle(ep.Handle) })
	before, err := rt.client.inspectContainer(ep.Handle.ContainerID)
	if err != nil {
		t.Fatal(err)
	}
	memory, cpu := 128, 150
	if err := rt.UpdateResources(context.Background(), ep.Handle, ResourceLimits{MemoryLimitMB: &memory, CPUQuotaPercent: &cpu}); err != nil {
		t.Fatal(err)
	}
	after, err := rt.client.inspectContainer(ep.Handle.ContainerID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Running || after.Pid != before.Pid {
		t.Fatalf("container replaced: before=%+v after=%+v", before, after)
	}
	var settings struct {
		HostConfig struct{ Memory, NanoCpus int64 }
	}
	if err := rt.client.get("/containers/"+ep.Handle.ContainerID+"/json", &settings); err != nil {
		t.Fatal(err)
	}
	if settings.HostConfig.Memory != 128*1024*1024 || settings.HostConfig.NanoCpus != 1_500_000_000 {
		t.Fatalf("limits not applied: %+v", settings)
	}
}

func TestDockerLiveResourceUpdateReportsDaemonFailure(t *testing.T) {
	rt := newDockerRuntimeWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "update failed", http.StatusInternalServerError)
	}))
	cpu := 150
	if err := rt.UpdateResources(context.Background(), RunHandle{ContainerID: "live"}, ResourceLimits{CPUQuotaPercent: &cpu}); err == nil {
		t.Fatal("daemon failure was hidden")
	}
}

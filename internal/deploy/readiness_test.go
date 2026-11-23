package deploy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

type readinessRuntime struct {
	resumeFakeRuntime
	endpoint string
	done     chan struct{}
	stop     sync.Once
}

func (r *readinessRuntime) Signal(process.RunHandle, syscall.Signal) error {
	r.stop.Do(func() { close(r.done) })
	return nil
}

func (r *readinessRuntime) Wait(ctx context.Context, _ process.RunHandle) error {
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *readinessRuntime) Start(context.Context, process.StartParams, io.Writer) (process.ReplicaEndpoint, error) {
	return process.ReplicaEndpoint{URL: r.endpoint, Provider: "fake", Handle: process.RunHandle{PID: 9}}, nil
}

func TestBootReplicaHonorsReadinessContract(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			bundle := t.TempDir()
			if err := os.WriteFile(filepath.Join(bundle, ManifestFilename), []byte("[app]\ncommand = ['/bin/true']\nreadiness_path = '/health/ready'\nreadiness_status = 204\n"), 0600); err != nil {
				t.Fatal(err)
			}
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/health/ready" {
					t.Errorf("boot probed %q instead of the declared readiness endpoint", r.URL.Path)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.WriteHeader(status)
			}))
			defer backend.Close()
			mgr := process.NewManager(t.TempDir(), &readinessRuntime{endpoint: backend.URL, done: make(chan struct{})})
			p := Params{Slug: "guarded", BundleDir: bundle, Manager: mgr, Proxy: proxy.New()}
			p.Proxy.SetPoolSize("guarded", 1)
			_, _, err := bootReplicaAttempt(p, 0, "", "", []string{"/bin/true"}, false, nil, 250*time.Millisecond)
			if status == http.StatusNoContent && err != nil {
				t.Fatalf("declared readiness rejected: %v", err)
			}
			if status != http.StatusNoContent && err == nil {
				t.Fatal("wrong readiness status accepted")
			}
			_ = mgr.Stop("guarded")
		})
	}
}

func TestProbeReadinessUsesBundleContract(t *testing.T) {
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, ManifestFilename), []byte("[app]\nreadiness_path = '/ready'\nreadiness_status = 204\n"), 0600); err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			t.Errorf("probe path = %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(204)
	}))
	defer backend.Close()
	if err := ProbeReadiness(context.Background(), backend.URL, bundle, nil); err != nil {
		t.Fatal(err)
	}
}

func TestProbeReadinessRejectsUnhealthyAndBoundsWait(t *testing.T) {
	for _, hung := range []bool{false, true} {
		t.Run(map[bool]string{false: "unhealthy", true: "hung"}[hung], func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if hung {
					<-r.Context().Done()
					return
				}
				w.WriteHeader(503)
			}))
			defer backend.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if err := ProbeReadiness(ctx, backend.URL, t.TempDir(), nil); err == nil {
				t.Fatal("unready endpoint accepted")
			}
		})
	}
}

package deploy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// resumeFakeRuntime implements Runtime + Snapshotter without launching a real
// process: Start returns a synthetic endpoint, Wait blocks (the "process" stays
// alive), and Suspend/Resume are scripted. It lets ResumeReplica be tested
// through the real Manager.Start/Suspend public path - no production test seam.
type resumeFakeRuntime struct {
	process.NativeRuntime
	resumeEP  process.ReplicaEndpoint
	resumeErr error
}

func (f *resumeFakeRuntime) Start(_ context.Context, _ process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	return process.ReplicaEndpoint{URL: "http://127.0.0.1:2500", Provider: "fake", Handle: process.RunHandle{PID: 9}}, nil
}

func (f *resumeFakeRuntime) Wait(ctx context.Context, _ process.RunHandle) error {
	<-ctx.Done() // never cancelled by Manager.Start; the entry stays alive
	return ctx.Err()
}

func (f *resumeFakeRuntime) Suspend(context.Context, process.RunHandle) (bool, error) {
	return true, nil
}

func (f *resumeFakeRuntime) Resume(context.Context, process.RunHandle) (process.ReplicaEndpoint, error) {
	return f.resumeEP, f.resumeErr
}

func startAndSuspend(t *testing.T, fake *resumeFakeRuntime) *process.Manager {
	t.Helper()
	mgr := process.NewManager(t.TempDir(), fake)
	if _, err := mgr.Start(process.StartParams{
		Slug: "app", Index: 0, Command: []string{"true"}, Dir: t.TempDir(), Port: 2500,
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := mgr.Suspend("app"); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	return mgr
}

func TestResumeReplica_RegistersAfterProbe(t *testing.T) {
	fake := &resumeFakeRuntime{
		resumeEP: process.ReplicaEndpoint{URL: "http://127.0.0.1:2500", Provider: "fake", Handle: process.RunHandle{PID: 9}},
	}
	mgr := startAndSuspend(t, fake)
	prx := proxy.New()
	prx.SetPoolSize("app", 1)

	var probed string
	p := Params{
		Slug:    "app",
		Manager: mgr,
		Proxy:   prx,
		HealthCheck: func(url string, _ time.Duration, _ http.RoundTripper) error {
			probed = url
			return nil
		},
	}
	res, err := ResumeReplica(p, 0)
	if err != nil {
		t.Fatalf("ResumeReplica: %v", err)
	}
	if probed != "http://127.0.0.1:2500" {
		t.Fatalf("probe url = %q", probed)
	}
	if res.EndpointURL != "http://127.0.0.1:2500" {
		t.Fatalf("result url = %q", res.EndpointURL)
	}
}

func TestResumeReplica_NotSuspendedReturnsSentinel(t *testing.T) {
	fake := &resumeFakeRuntime{}
	mgr := process.NewManager(t.TempDir(), fake)
	if _, err := mgr.Start(process.StartParams{
		Slug: "app", Index: 0, Command: []string{"true"}, Dir: t.TempDir(), Port: 2500,
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	// running, never suspended
	p := Params{Slug: "app", Manager: mgr, Proxy: proxy.New()}

	if _, err := ResumeReplica(p, 0); !errors.Is(err, process.ErrReplicaNotSuspended) {
		t.Fatalf("err = %v, want ErrReplicaNotSuspended", err)
	}
}

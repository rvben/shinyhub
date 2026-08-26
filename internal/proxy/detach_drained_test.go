package proxy_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/proxy"
)

func TestDetachDrainedReplica_IsTargetAndDrainGated(t *testing.T) {
	p := proxy.New()
	p.SetPoolSize("demo", 1)
	const target = "http://127.0.0.1:9123"
	if err := p.RegisterReplica("demo", 0, target, nil, 1); err != nil {
		t.Fatal(err)
	}
	if got := p.DetachDrainedReplica("demo", 0, target, false); got.Matched || got.Detached {
		t.Fatalf("non-draining detach = %+v, want no match", got)
	}
	if !p.DrainReplica("demo", 0) {
		t.Fatal("failed to mark replica draining")
	}
	if got := p.DetachDrainedReplica("demo", 0, "http://stale", false); got.Matched || got.Detached {
		t.Fatalf("stale-target detach = %+v, want no match", got)
	}
	if got := p.DetachDrainedReplica("demo", 0, target, false); !got.Matched || !got.Detached || got.ActiveConns != 0 {
		t.Fatalf("drained detach = %+v, want matched and detached", got)
	}
	if got := p.ReplicaTargetURL("demo", 0); got != "" {
		t.Fatalf("target after detach = %q, want empty", got)
	}
}

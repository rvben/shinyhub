package proxy

import (
	"io"
	"testing"
)

func trackedTestSession(p *Proxy, slug string, replica int, deploymentID int64, events chan<- WSSessionEnd) *trackedConn {
	s := &wsSession{}
	s.start(slug, replica, deploymentID, func(e WSSessionEnd) { events <- e })
	return p.conns.trackWithSession(&stubConn{}, ConnPrincipal{}, s.end, s).(*trackedConn)
}

func TestMarkElasticWorkerLifetimeMatchesWorkerGeneration(t *testing.T) {
	p := New()
	p.pools["demo"] = &backendPool{workers: map[int]*replicaBackend{9: {deploymentID: 42}}}
	events := make(chan WSSessionEnd, 3)
	matching := trackedTestSession(p, "demo", 9, 42, events)
	otherDeployment := trackedTestSession(p, "demo", 9, 41, events)
	otherReplica := trackedTestSession(p, "demo", 8, 42, events)
	clear := p.MarkElasticWorkerLifetime("demo", 9)
	_ = matching.Close()
	e := <-events
	if e.ClosedBy != "lifetime" || e.Abnormal || e.ReplicaIndex != 9 || e.DeploymentID != 42 {
		t.Fatalf("lifetime event = %+v", e)
	}
	clear() // A completed event is immutable.
	_ = otherDeployment.Close()
	_ = otherReplica.Close()
	for i := 0; i < 2; i++ {
		if e := <-events; e.ClosedBy == "lifetime" {
			t.Fatalf("marked another worker: %+v", e)
		}
	}
}

func TestMarkGenerationDrainCanBeClearedAfterFailedStop(t *testing.T) {
	p := New()
	events := make(chan WSSessionEnd, 2)
	c := trackedTestSession(p, "demo", 9, 42, events)
	clear := p.MarkGenerationDrain("demo", 42)
	clear()
	c.session.observeRead("upstream", nil, io.EOF)
	_ = c.Close()
	if e := <-events; e.ClosedBy != "unknown" || !e.Abnormal {
		t.Fatalf("failed stop left drain mark: %+v", e)
	}
}

func TestMarkGenerationDrainPreservesClientCloseFrame(t *testing.T) {
	p := New()
	events := make(chan WSSessionEnd, 1)
	c := trackedTestSession(p, "demo", 9, 42, events)
	_ = p.MarkGenerationDrain("demo", 42)
	c.session.observeRead("client", []byte{0x88, 0x82, 1, 2, 3, 4, 0x03 ^ 1, 0xe8 ^ 2}, nil)
	_ = c.Close()
	if e := <-events; e.ClosedBy != "client" || e.Abnormal {
		t.Fatalf("client close was overridden by planned drain: %+v", e)
	}
}

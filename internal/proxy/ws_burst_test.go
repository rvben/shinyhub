package proxy

import (
	"testing"
	"time"
)

func TestWSAbnormalBurstDetectorGroupsWorkerAndSuppressesDuplicates(t *testing.T) {
	d := NewWSAbnormalBurstDetector()
	now := time.Unix(1000, 0)
	d.now = func() time.Time { return now }
	e := WSSessionEnd{Slug: "demo", ReplicaIndex: 9, DeploymentID: 42, Abnormal: true}
	for i := 0; i < 2; i++ {
		if _, ok := d.Record(e); ok {
			t.Fatal("warned before threshold")
		}
		now = now.Add(time.Millisecond)
	}
	burst, ok := d.Record(e)
	if !ok || burst.Count != 3 || burst.Replica != 9 || burst.Window != wsBurstWindow || burst.Span != 2*time.Millisecond {
		t.Fatalf("third close = %+v, %t", burst, ok)
	}
	if _, ok := d.Record(e); ok {
		t.Fatal("duplicate warning inside cooldown")
	}
	otherWorker := e
	otherWorker.ReplicaIndex = 10
	if _, ok := d.Record(otherWorker); ok {
		t.Fatal("events on another worker shared the window")
	}
	otherDeployment := e
	otherDeployment.DeploymentID = 43
	if _, ok := d.Record(otherDeployment); ok {
		t.Fatal("events in another deployment shared the window")
	}
	for i := 0; i < 3; i++ {
		now = now.Add(time.Minute)
		if _, ok := d.Record(e); ok {
			t.Fatal("closes spread over minutes formed a burst")
		}
	}
}

func TestWSAbnormalBurstDetectorOnlyReportsSharedCloseReason(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		d := NewWSAbnormalBurstDetector()
		code := uint16(1011)
		e := WSSessionEnd{Slug: "demo", ReplicaIndex: 9, DeploymentID: 42,
			Abnormal: true, CloseCode: &code, CloseReason: "keepalive ping timeout"}
		_, _ = d.Record(e)
		_, _ = d.Record(e)
		if mixed {
			e.CloseReason = "worker stopped"
		}
		burst, ok := d.Record(e)
		if !ok {
			t.Fatal("missing burst")
		}
		if mixed && (burst.CloseCode != nil || burst.CloseReason != "") {
			t.Fatalf("mixed causes presented as shared: %+v", burst)
		}
		if !mixed && (burst.CloseCode == nil || *burst.CloseCode != 1011 || burst.CloseReason != "keepalive ping timeout") {
			t.Fatalf("shared close reason omitted: %+v", burst)
		}
	}
}

func TestWSAbnormalBurstDetectorIgnoresPlannedAndNormalEnds(t *testing.T) {
	d := NewWSAbnormalBurstDetector()
	for _, closedBy := range []string{"lifetime", "drain", "client"} {
		for i := 0; i < 3; i++ {
			if _, ok := d.Record(WSSessionEnd{Slug: "demo", ReplicaIndex: 9, DeploymentID: 42, ClosedBy: closedBy}); ok {
				t.Fatalf("%s contributed to abnormal burst", closedBy)
			}
		}
	}
}

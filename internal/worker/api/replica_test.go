package api

import (
	"encoding/json"
	"testing"
)

func TestReplicaStartRequest_RoundTrip(t *testing.T) {
	in := ReplicaStartRequest{
		Slug:             "app",
		Index:            2,
		Tier:             "remote",
		ContentDigest:    "sha256:abc",
		AppVersion:       "v1",
		DeploymentID:     42,
		Command:          []string{"./server", "--port", "8080"},
		Env:              map[string]string{"PORT": "8080"},
		BindPort:         8080,
		SharedMountSlugs: []string{"data"},
		MemoryLimitMB:    256,
		CPUQuotaPercent:  50,
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out ReplicaStartRequest
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Slug != in.Slug || out.BindPort != in.BindPort || out.DeploymentID != 42 {
		t.Errorf("round trip mismatch: %+v != %+v", out, in)
	}
	if len(out.Command) != 3 || out.Command[2] != "8080" {
		t.Errorf("command not preserved: %v", out.Command)
	}
	if out.SharedMountSlugs[0] != "data" {
		t.Errorf("shared mount slug not preserved: %v", out.SharedMountSlugs)
	}
}

func TestReplicaResult_RoundTrip(t *testing.T) {
	in := ReplicaResult{NodeID: "node-a", ContainerID: "c123", URL: "https://adv/v1/data/tok"}
	b, _ := json.Marshal(in)
	var out ReplicaResult
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("round trip mismatch: %+v != %+v", out, in)
	}
}

func TestStatsResultAndExitResult_RoundTrip(t *testing.T) {
	s := StatsResult{CPUPercent: 12.5, RSSBytes: uint64(1 << 20)}
	b, _ := json.Marshal(s)
	var so StatsResult
	if err := json.Unmarshal(b, &so); err != nil || so != s {
		t.Fatalf("stats round trip: %v %+v", err, so)
	}
	e := ExitResult{Code: 137, Signaled: true}
	b, _ = json.Marshal(e)
	var eo ExitResult
	if err := json.Unmarshal(b, &eo); err != nil || eo != e {
		t.Fatalf("exit round trip: %v %+v", err, eo)
	}
}

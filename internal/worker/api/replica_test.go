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
	cpu := 12.5
	s := StatsResult{CPUPercent: &cpu, RSSBytes: uint64(1 << 20)}
	b, _ := json.Marshal(s)
	var so StatsResult
	if err := json.Unmarshal(b, &so); err != nil {
		t.Fatalf("stats round trip: %v", err)
	}
	if so.CPUPercent == nil || *so.CPUPercent != cpu || so.RSSBytes != s.RSSBytes {
		t.Fatalf("stats round trip: got %+v", so)
	}
	e := ExitResult{Code: 137, Signaled: true}
	b, _ = json.Marshal(e)
	var eo ExitResult
	if err := json.Unmarshal(b, &eo); err != nil || eo != e {
		t.Fatalf("exit round trip: %v %+v", err, eo)
	}
}

// TestStatsResultCPUAbsentOnTheWire pins the encoding of an unavailable rate,
// which crosses a binary boundary: the server decodes what a worker sends, and
// the two can be different builds mid-upgrade.
func TestStatsResultCPUAbsentOnTheWire(t *testing.T) {
	b, err := json.Marshal(StatsResult{RSSBytes: 4096})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The key stays present and carries null. Omitting it entirely would let a
	// decoder leave a stale value in place, and "unknown" would become whatever
	// the last poll happened to say.
	if got, want := string(b), `{"cpu_percent":null,"rss_bytes":4096}`; got != want {
		t.Errorf("wire form = %s, want %s", got, want)
	}

	var out StatsResult
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.CPUPercent != nil {
		t.Errorf("decoded cpu = %v, want nil", *out.CPUPercent)
	}

	// A worker on an older build sends a number unconditionally, including the
	// 0 it used to report when it had no baseline. That decodes to a non-nil
	// rate, which is exactly what the field meant before, so a mixed-version
	// pair degrades to the old behaviour rather than failing to decode.
	var legacy StatsResult
	if err := json.Unmarshal([]byte(`{"cpu_percent":0,"rss_bytes":4096}`), &legacy); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if legacy.CPUPercent == nil || *legacy.CPUPercent != 0 {
		t.Errorf("legacy cpu = %v, want a non-nil 0", legacy.CPUPercent)
	}
}

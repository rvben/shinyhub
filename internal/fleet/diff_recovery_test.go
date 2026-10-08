package fleet

import "testing"

func TestDiffMatchingContentDeploymentRecovery(t *testing.T) {
	for _, tc := range []struct {
		name       string
		repair     *bool
		status     string
		desired    string
		owner      *string
		config     bool
		local      string
		wantAction Action
		wantReason bool
		warning    bool
	}{
		{"failed barrier", bp(true), "failed", "failed", sp("fleet:eu"), false, "same", ActionUpdateSource, true, false},
		{"failed barrier and config", bp(true), "failed", "failed", sp("fleet:eu"), true, "same", ActionUpdateSourceConfig, true, false},
		{"adopt failed barrier", bp(true), "failed", "failed", nil, false, "same", ActionAdopt, true, false},
		{"pending on another instance", bp(true), "pending", "failed", sp("fleet:eu"), false, "same", ActionUnchanged, false, false},
		{"deliberately stopped", bp(true), "failed", "stopped", sp("fleet:eu"), false, "same", ActionUnchanged, false, true},
		{"old server", nil, "failed", "failed", sp("fleet:eu"), false, "same", ActionUnchanged, false, true},
		{"writer-only failure", bp(false), "succeeded", "failed", sp("fleet:eu"), false, "same", ActionUnchanged, false, false},
		{"healthy failed attempt", bp(false), "failed", "running", sp("fleet:eu"), false, "same", ActionUnchanged, false, false},
		{"repaired", bp(false), "succeeded", "running", sp("fleet:eu"), false, "same", ActionUnchanged, false, false},
		{"hibernated", bp(false), "succeeded", "hibernated", sp("fleet:eu"), false, "same", ActionUnchanged, false, false},
		{"suspended", bp(false), "succeeded", "suspended", sp("fleet:eu"), false, "same", ActionUnchanged, false, false},
		{"source drift already deploys", bp(true), "failed", "failed", sp("fleet:eu"), false, "different", ActionUpdateSource, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := AppEntry{Slug: "app", Visibility: "private"}
			if tc.config {
				entry.Bundle.MemoryLimitMB = ptr(512)
			}
			m := &Manifest{FleetID: "eu", Apps: []AppEntry{entry}}
			obs := ObservedApp{Slug: "app", Access: "private", ContentDigest: "same", ManagedBy: tc.owner,
				DeploymentRepairRequired: tc.repair, LastDeploymentStatus: tc.status, DesiredStatus: tc.desired}
			if tc.config {
				obs.MemoryLimitMB = ptr(256)
			}
			d := Diff(m, map[string]string{"app": tc.local}, []ObservedApp{obs})[0]
			if d.Action != tc.wantAction || (d.RecoveryReason == RecoveryFailed) != tc.wantReason || (len(d.Warnings) > 0) != tc.warning {
				t.Fatalf("diff=%+v", d)
			}
		})
	}
}

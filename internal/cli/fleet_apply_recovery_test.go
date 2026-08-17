package cli

import (
	"strings"
	"testing"
)

func TestFleetApplyRecoveryCommandPreservesIntentWithoutPreconfirming(t *testing.T) {
	f := &fleetApplyFlags{
		file: "env/eu fleet.toml", adopt: true, prune: true, yes: true,
		allowUnsafeDegradedPrune: true, retries: 2, healthTimeout: 90,
		restartAfterWarm: true, concurrency: 1,
	}
	got := fleetApplyRecoveryCommand(f)
	for _, want := range []string{
		"--adopt", "--prune", "--allow-unsafe-degraded-prune", "--retries 2",
		"--health-timeout 90", "--restart-after-warm", "--concurrency 1",
		"-f 'env/eu fleet.toml'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("recovery command %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "--yes") {
		t.Fatalf("recovery command must never pre-confirm deletion: %q", got)
	}
}

package lifecycle

import (
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
)

// TestExitReason_LiveCrashVsRestartDiscoveredCrash is the OR-F8 regression: a
// replica that crashes while this server is watching it gets a rich, specific
// verdict from the live exit monitor (replicaExitVerdict in
// internal/process/manager.go), because that code path actually called
// wait(2) on the process and can name the exit code. A replica that crashed
// while this server was DOWN and is found dead at restart goes through
// recoverNativeReplica instead, which never waited on the process and
// therefore cannot know its exit code, signal, or OOM status.
//
// The two reasons are asserted to differ (parity would mean one of them is
// faking specificity it does not have), and the restart-path row is asserted
// to carry no fabricated exit code/signal/OOM fact - only an explicit,
// honestly-worded "unknown" reason. This is the "absent data must not become
// a plausible value" rule applied to crash diagnostics.
func TestExitReason_LiveCrashVsRestartDiscoveredCrash(t *testing.T) {
	// --- Live path: a real process this server starts, watches, and reaps. ---
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	if _, err := mgr.Start(process.StartParams{
		Slug:    "live-crash",
		Dir:     t.TempDir(),
		Command: []string{"sh", "-c", "exit 1"},
		Port:    19245,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var liveVerdict process.ExitVerdict
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := mgr.LastExit("live-crash", 0); ok {
			liveVerdict = v
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if liveVerdict.Reason == "" {
		t.Fatal("expected a LastExit verdict after the live replica exited")
	}
	if liveVerdict.ExitCode == nil || *liveVerdict.ExitCode != 1 {
		t.Fatalf("live verdict ExitCode = %v, want 1 (the live path observed the real exit)", liveVerdict.ExitCode)
	}

	// --- Restart path: a replica row for a PID that is provably not running,
	// discovered only after this server (re)started - no wait(2) ever happened. ---
	store, app := seedWarmApp(t)
	deadPID, port := 99999999, 20099
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, PID: &deadPID, Port: &port,
		Status: db.ReplicaStatusRunning, DesiredState: "running",
		Provider: "native", Tier: "default",
	}); err != nil {
		t.Fatal(err)
	}

	restartMgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	adopted := recoverNativeReplica(store, restartMgr, nil, app, replicaAt(t, store, app.ID, 0), t.TempDir(), "")
	if adopted {
		t.Fatal("a replica with a dead PID must not be reported as adopted")
	}

	rep := replicaAt(t, store, app.ID, 0)
	if rep.LastExit == nil {
		t.Fatal("expected a crash diagnostic to be recorded for the dead replica")
	}
	restartReason := rep.LastExit.Reason
	if restartReason == "" {
		t.Fatal("restart-discovered crash must record a non-empty reason, not silently drop the fact of the crash")
	}

	// The honest difference: the restart path must never claim the live path's
	// specificity (an exit code it never observed).
	if restartReason == liveVerdict.Reason {
		t.Fatalf("restart-path reason (%q) must not equal the live-path reason (%q): true parity here can only mean one of them fabricated a fact it does not have",
			restartReason, liveVerdict.Reason)
	}
	if strings.Contains(restartReason, "code") || strings.Contains(restartReason, "signal") || strings.Contains(restartReason, "SIG") {
		t.Fatalf("restart-path reason %q looks like a specific exit classification the recovery path cannot actually know", restartReason)
	}
	if !strings.Contains(restartReason, "unknown") {
		t.Fatalf("restart-path reason %q must say plainly that the cause is unknown, not read like a specific diagnosis", restartReason)
	}

	// No fabricated exit code, signal, or OOM fact - only the honest reason.
	if rep.LastExit.ExitCode != nil {
		t.Errorf("restart-path ExitCode = %v, want nil (never observed, must not be coerced to a value)", rep.LastExit.ExitCode)
	}
	if rep.LastExit.Signal != "" {
		t.Errorf("restart-path Signal = %q, want empty (never observed, must not be coerced to a value)", rep.LastExit.Signal)
	}
	if rep.LastExit.OOMKilled {
		t.Error("restart-path OOMKilled = true, want false (never observed, must not be coerced to a value)")
	}
}

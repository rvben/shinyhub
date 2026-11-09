package main

import (
	"testing"
)

// The owner-startup relaunch of owed settings redeploys runs on its own
// goroutine from ownerRuntime.run, which cannot be unit imported. Pin it to the
// panic-safe root so a panic in the scan cannot crash the server.
func TestMainRelaunchesOwedRedeploysUnderSafego(t *testing.T) {
	for _, path := range []string{"main.go", "owner_runtime.go"} {
		if n := sourceCount(t, path, "go srv.RelaunchOwedRedeploys()"); n != 0 {
			t.Fatalf("%s launches RelaunchOwedRedeploys on a bare goroutine; use safego.Go", path)
		}
	}
	sourceIndex(t, "owner_runtime.go", `safego.Go("relaunch owed redeploys", srv.RelaunchOwedRedeploys)`)
}

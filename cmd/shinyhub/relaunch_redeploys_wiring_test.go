package main

import (
	"os"
	"strings"
	"testing"
)

// The owner-startup relaunch of owed settings redeploys runs on its own
// goroutine from main.go, which cannot be unit imported. Pin it to the
// panic-safe root so a panic in the scan cannot crash the server.
func TestMainRelaunchesOwedRedeploysUnderSafego(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	s := string(src)
	if strings.Contains(s, "go srv.RelaunchOwedRedeploys()") {
		t.Fatal("main.go launches RelaunchOwedRedeploys on a bare goroutine; use safego.Go")
	}
	if want := `safego.Go("relaunch owed redeploys", srv.RelaunchOwedRedeploys)`; strings.Count(s, want) != 1 {
		t.Fatalf("main.go must launch the owed-redeploy relaunch with %s exactly once", want)
	}
}

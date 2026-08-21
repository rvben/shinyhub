package main

import (
	"os"
	"strings"
	"testing"
)

// The render-admission startup wiring lives in main.go, which cannot be unit
// imported. Pin the load-bearing calls by source search so a refactor that drops
// one fails the build instead of silently disabling the feature.
func TestMainWiresRenderAdmission(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	s := string(src)
	for _, needle := range []string{
		"admission.Detect(",        // effective cores are detected
		"admission.NewWatermark(",  // the watermark is constructed
		"wm.Run(ctx)",              // and its background sampler is started on the server ctx
		"proxy.BuildAppLimiter(",   // per-app limiters are sized (now inside the factory)
		"SetCPUWatermark(",         // and installed on the proxy
		"SetAppAccessLookup(",      // the access-mode lookup is wired for per-principal fairness
		"SetRenderLimiterFactory(", // the limiter factory is installed
		"ApplyRenderPacing(",       // every app is paced at startup through it
		"SetRenderPacingCores(",    // the API server gets the cap-suggestion cores
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("main.go is missing the render-admission wiring call %q", needle)
		}
	}
}

// The Overview's resource panel measures a limit-free fleet against the host
// itself. That denominator is detected once at startup in main.go and handed to
// the API server; without these two calls the panel has no capacity to report
// and every row degrades to "Capacity unavailable".
func TestMainWiresHostCapacity(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	s := string(src)
	for _, needle := range []string{
		"admission.DetectMemory(",    // host memory is detected (cgroup limit, then host total)
		"cfg.HostCapacityCores()",    // the operator's core override is honoured
		"cfg.HostCapacityMemoryMB()", // and the memory override with it
		"srv.SetHostCapacity(",       // the result reaches the API server
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("main.go is missing the host-capacity wiring call %q", needle)
		}
	}
}

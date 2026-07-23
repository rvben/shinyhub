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
		"admission.Detect(",       // effective cores are detected
		"admission.NewWatermark(", // the watermark is constructed
		".Run(",                   // and its background sampler is started
		"proxy.BuildAppLimiter(",  // per-app limiters are sized
		"SetCPUWatermark(",        // and installed on the proxy
		"SetAppLimiter(",
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("main.go is missing the render-admission wiring call %q", needle)
		}
	}
}

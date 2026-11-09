package main

import (
	"os"
	"strings"
	"testing"
)

// The render-admission startup wiring lives in configureProxyAdmission, whose
// effects sit behind unexported proxy and API state. Pin the load-bearing calls
// by source search so a refactor that drops one fails the build instead of
// silently disabling the feature, and require runServe to call it.
func TestMainWiresRenderAdmission(t *testing.T) {
	if n := sourceCount(t, "main.go", "configureProxyAdmission(cfg, store, srv, prx, services)"); n != 1 {
		t.Fatalf("runServe calls configureProxyAdmission %d times, want exactly once", n)
	}
	s := proxyAdmissionSource(t)
	for _, needle := range []string{
		"admission.Detect(",          // effective cores are detected
		"admission.NewWatermark(",    // the watermark is constructed
		"wm.Run)",                    // and its sampler runs as a service of the run group
		"proxy.BuildAppLimiter(",     // per-app limiters are sized (now inside the factory)
		"SetCPUWatermark(",           // and installed on the proxy
		"SetAppAccessLookup(",        // the access-mode lookup is wired for per-principal fairness
		"cfg.RenderPrincipalBurst()", // the configured principal burst reaches limiter sizing
		"SetRenderLimiterFactory(",   // the limiter factory is installed
		"ApplyRenderPacing(",         // every app is paced at startup through it
		"SetRenderPacingCores(",      // the API server gets the cap-suggestion cores
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("proxy_admission.go is missing the render-admission wiring call %q", needle)
		}
	}
}

// The Overview's resource panel measures a limit-free fleet against the host
// itself. That denominator is detected once at startup and handed to
// the API server; without these two calls the panel has no capacity to report
// and every row degrades to "Capacity unavailable".
func TestMainWiresHostCapacity(t *testing.T) {
	s := proxyAdmissionSource(t)
	for _, needle := range []string{
		"admission.DetectMemory(",    // host memory is detected (cgroup limit, then host total)
		"cfg.HostCapacityCores()",    // the operator's core override is honoured
		"cfg.HostCapacityMemoryMB()", // and the memory override with it
		"srv.SetHostCapacity(",       // the result reaches the API server
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("proxy_admission.go is missing the host-capacity wiring call %q", needle)
		}
	}
}

func proxyAdmissionSource(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("proxy_admission.go")
	if err != nil {
		t.Fatalf("read proxy_admission.go: %v", err)
	}
	return string(src)
}

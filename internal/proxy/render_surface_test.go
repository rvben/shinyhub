package proxy

import (
	"strings"
	"testing"
)

// TestRenderRejectReasons pins the two render-admission reject reasons the
// charge-point plan will stamp on paced/shed sessions. Both must stay
// distinct from ReasonPoolSaturated so capacity automation never mistakes a
// render pace-limit or a CPU watermark breach for a signal to add replicas.
func TestRenderRejectReasons(t *testing.T) {
	if ReasonRenderPaced != "render-paced" {
		t.Fatalf("ReasonRenderPaced = %q, want render-paced", ReasonRenderPaced)
	}
	if ReasonCPUSaturation != "cpu-saturation" {
		t.Fatalf("ReasonCPUSaturation = %q, want cpu-saturation", ReasonCPUSaturation)
	}
	// Distinct from the scale-up signal, so capacity automation never reads
	// either as demand for more replicas.
	if ReasonRenderPaced == ReasonPoolSaturated || ReasonCPUSaturation == ReasonPoolSaturated {
		t.Fatal("render reject reasons must be distinct from pool-saturated")
	}
}

// TestWaitingPageRenders checks waitingPage is a real HTML wait page (shares
// the waitPage shell) carrying the capacity-specific title.
func TestWaitingPageRenders(t *testing.T) {
	if waitingPage == "" {
		t.Fatal("waitingPage must be rendered")
	}
	for _, want := range []string{"Waiting for capacity", "shinyhub-box", "<html"} {
		if !strings.Contains(waitingPage, want) {
			t.Fatalf("waitingPage missing %q", want)
		}
	}
}

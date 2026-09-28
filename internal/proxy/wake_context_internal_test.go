package proxy

import (
	"context"
	"net/http/httptest"
	"testing"
)

// TestWakeContext_NoSpanAllocatesNothing pins the tracing-disabled contract on
// the wake trigger sites: a request context with no span costs no allocation
// to hand to the trigger.
func TestWakeContext_NoSpanAllocatesNothing(t *testing.T) {
	ctx := httptest.NewRequest("GET", "/app/demo/", nil).Context()
	var got context.Context
	allocs := testing.AllocsPerRun(100, func() { got = wakeContext(ctx) })
	if allocs != 0 {
		t.Fatalf("wakeContext allocated %.1f times per call without a span, want 0", allocs)
	}
	if got != context.Background() {
		t.Fatalf("wakeContext without a span = %v, want context.Background()", got)
	}
}

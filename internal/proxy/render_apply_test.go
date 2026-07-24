package proxy

import (
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/admission"
)

func testFactory() func(float64) *admission.AppLimiter {
	// Mirrors main.go's production sizing: 2 cores, 0.75 headroom, principalBurst 3.
	return func(rs float64) *admission.AppLimiter {
		return BuildAppLimiter(rs, 2, 0.75, 3, 20, 4096)
	}
}

func TestApplyRenderPacing_InstallsAndClears(t *testing.T) {
	p := New()
	p.SetRenderLimiterFactory(testFactory())

	// From off (no limiter) to on installs a limiter.
	p.ApplyRenderPacing("demo", 1.3)
	if p.appLimiter("demo") == nil {
		t.Fatal("ApplyRenderPacing(1.3) should install a limiter")
	}
	// Back to 0 clears it.
	p.ApplyRenderPacing("demo", 0)
	if p.appLimiter("demo") != nil {
		t.Fatal("ApplyRenderPacing(0) should clear the limiter")
	}
}

func TestApplyRenderPacing_UnchangedDoesNotRebuild(t *testing.T) {
	p := New()
	p.SetRenderLimiterFactory(testFactory())
	p.ApplyRenderPacing("demo", 1.3)
	first := p.appLimiter("demo")
	// Same value again: must be the SAME limiter pointer (no rebuild, tokens kept).
	p.ApplyRenderPacing("demo", 1.3)
	if p.appLimiter("demo") != first {
		t.Fatal("ApplyRenderPacing with an unchanged value must not rebuild the limiter")
	}
	// A different value rebuilds.
	p.ApplyRenderPacing("demo", 2.6)
	if p.appLimiter("demo") == first {
		t.Fatal("ApplyRenderPacing with a changed value must install a new limiter")
	}
}

func TestApplyRenderPacing_NilFactoryIsSafe(t *testing.T) {
	p := New() // no factory set
	p.ApplyRenderPacing("demo", 1.3)
	if p.appLimiter("demo") != nil {
		t.Fatal("with no factory, ApplyRenderPacing must not install a limiter")
	}
}

func TestApplyRenderPacing_ConcurrentIsRaceCleanAndFinalWins(t *testing.T) {
	p := New()
	p.SetRenderLimiterFactory(testFactory())
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				p.ApplyRenderPacing("demo", 1.3)
			} else {
				p.ApplyRenderPacing("demo", 0)
			}
			_ = p.appLimiter("demo") // concurrent reader, as the charge path does
		}(i)
	}
	wg.Wait()
	// A definitive final write must win and be observable (last-write-wins).
	p.ApplyRenderPacing("demo", 1.3)
	if p.appLimiter("demo") == nil {
		t.Fatal("after a final ApplyRenderPacing(1.3), the limiter must be installed")
	}
	p.ApplyRenderPacing("demo", 0)
	if p.appLimiter("demo") != nil {
		t.Fatal("after a final ApplyRenderPacing(0), the limiter must be cleared")
	}
}

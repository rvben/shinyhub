package leader

import (
	"context"
	"testing"
)

// TestOwnerScope_RecoversWorkPanic proves a panic in owner-only work is
// recovered rather than crashing the whole process (an unrecovered goroutine
// panic is fatal). If recovery were missing, this test binary would abort
// instead of Lose() returning cleanly (PROD-1).
func TestOwnerScope_RecoversWorkPanic(t *testing.T) {
	s := NewOwnerScope(func(ctx context.Context, epoch int64) {
		panic("boom")
	})
	s.Acquire(1)
	s.Lose() // blocks until the (recovered) work goroutine finishes
}

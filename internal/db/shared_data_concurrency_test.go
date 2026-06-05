package db_test

import (
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestGrantSharedData_OpposingGrantsSerialize fires a->b and b->a concurrently
// and asserts exactly one succeeds (the other is rejected as a cycle), on both
// backends. On Postgres this exercises the advisory lock.
func TestGrantSharedData_OpposingGrantsSerialize(t *testing.T) {
	s := dbtest.New(t)
	owner := mustCreateUser(t, s, "owner", "admin")
	a := mustCreateApp(t, s, "app-a", owner.ID)
	b := mustCreateApp(t, s, "app-b", owner.ID)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = s.GrantSharedData(a.ID, b.ID) }()
	go func() { defer wg.Done(); errs[1] = s.GrantSharedData(b.ID, a.ID) }()
	wg.Wait()

	ok := 0
	cycle := 0
	for _, e := range errs {
		switch {
		case e == nil:
			ok++
		case e == db.ErrSharedDataCycle:
			cycle++
		default:
			// A duplicate or transient error is acceptable only if the other won;
			// fail loudly otherwise so a real race surfaces.
			t.Logf("grant returned: %v", e)
		}
	}
	if ok != 1 || cycle < 1 {
		t.Fatalf("expected exactly one grant to win and the other rejected as a cycle; got ok=%d cycle=%d errs=%v", ok, cycle, errs)
	}
}

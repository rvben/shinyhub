package db_test

import (
	"fmt"
	"sort"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// scopedSlugsOf reduces a page to its slugs, sorted, so a result set can be
// compared against a wanted set independent of row order.
func scopedSlugsOf(apps []*db.App) []string {
	out := make([]string, len(apps))
	for i, a := range apps {
		out[i] = a.Slug
	}
	sort.Strings(out)
	return out
}

func sameSlugs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestListAppsInSlugs_ExactAllowlist_NoLeakage proves the allowlist-scoped
// listing returns exactly the apps named in the allowlist and nothing else: a
// slug outside the allowlist never leaks in, and an unknown slug inside the
// allowlist is silently absent rather than erroring.
func TestListAppsInSlugs_ExactAllowlist_NoLeakage(t *testing.T) {
	store := dbtest.New(t)
	owner := mustCreateUser(t, store, "owner", "developer")
	for _, slug := range []string{"alpha", "bravo", "charlie", "delta", "echo"} {
		mustCreateApp(t, store, slug, owner.ID)
	}

	allow := []string{"bravo", "delta", "no-such-app"}
	apps, err := store.ListAppsInSlugs(allow, 0, 0)
	if err != nil {
		t.Fatalf("ListAppsInSlugs: %v", err)
	}
	want := []string{"bravo", "delta"}
	if got := scopedSlugsOf(apps); !sameSlugs(got, want) {
		t.Fatalf("ListAppsInSlugs(%v) = %v, want %v (allowed rows exactly, unknown slug absent, no leakage)", allow, got, want)
	}

	n, err := store.CountAppsInSlugs(allow)
	if err != nil {
		t.Fatalf("CountAppsInSlugs: %v", err)
	}
	if n != 2 {
		t.Fatalf("CountAppsInSlugs(%v) = %d, want 2", allow, n)
	}

	// A slug that exists but was never named in the allowlist must never appear,
	// confirming this is not "everything except explicitly excluded" but a true
	// allowlist.
	if got := scopedSlugsOf(apps); contains(got, "alpha") || contains(got, "charlie") || contains(got, "echo") {
		t.Fatalf("ListAppsInSlugs leaked an app outside the allowlist: %v", got)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// TestListAppsVisibleToUserInSlugs_IntersectsVisibilityAndAllowlist proves the
// visibility-scoped allowlist listing is the intersection of "visible to this
// user" and "in the allowlist", not either predicate alone: an app the caller
// cannot see must not leak in even when it is in the allowlist, and an app the
// caller can see must not leak in when it is outside the allowlist.
func TestListAppsVisibleToUserInSlugs_IntersectsVisibilityAndAllowlist(t *testing.T) {
	store := dbtest.New(t)
	caller := mustCreateUser(t, store, "caller", "developer")
	other := mustCreateUser(t, store, "other", "developer")

	mustCreateApp(t, store, "own-private", caller.ID)  // visible (owner), will be in allowlist
	mustCreateApp(t, store, "own-excluded", caller.ID) // visible (owner), NOT in allowlist

	otherPublic := mustCreateApp(t, store, "other-public", other.ID)
	if err := store.SetAppAccess(otherPublic.Slug, "public"); err != nil {
		t.Fatalf("set public: %v", err)
	} // visible (public), will be in allowlist

	otherPrivate := mustCreateApp(t, store, "other-private", other.ID) // NOT visible to caller, but WILL be in allowlist
	_ = otherPrivate

	allow := []string{"own-private", "other-public", "other-private"}
	apps, err := store.ListAppsVisibleToUserInSlugs(caller.ID, allow, 0, 0)
	if err != nil {
		t.Fatalf("ListAppsVisibleToUserInSlugs: %v", err)
	}
	want := []string{"other-public", "own-private"}
	if got := scopedSlugsOf(apps); !sameSlugs(got, want) {
		t.Fatalf("ListAppsVisibleToUserInSlugs(%v) = %v, want %v (visibility AND allowlist, no leakage of an invisible-but-allowlisted app, no leakage of a visible-but-not-allowlisted app)", allow, got, want)
	}

	n, err := store.CountAppsVisibleToUserInSlugs(caller.ID, allow)
	if err != nil {
		t.Fatalf("CountAppsVisibleToUserInSlugs: %v", err)
	}
	if n != 2 {
		t.Fatalf("CountAppsVisibleToUserInSlugs(%v) = %d, want 2", allow, n)
	}
}

// TestScopedListing_EmptyAllowlist_RunsNoSQL proves an empty allowlist returns
// empty (and a zero count) WITHOUT issuing any query. The store's underlying
// DB is closed first, so any of these methods reaching the database would
// surface as an error; a positive control (a non-empty allowlist on the same
// closed store) proves the technique actually detects a query attempt, so the
// empty-case success is not vacuous.
func TestScopedListing_EmptyAllowlist_RunsNoSQL(t *testing.T) {
	store := dbtest.New(t)
	owner := mustCreateUser(t, store, "owner", "developer")
	mustCreateApp(t, store, "irrelevant", owner.ID)

	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Positive control: a non-empty allowlist against the closed store must
	// error, proving the closed-DB probe can actually observe a query attempt.
	if _, err := store.ListAppsInSlugs([]string{"irrelevant"}, 0, 0); err == nil {
		t.Fatal("ListAppsInSlugs with a non-empty allowlist on a closed store returned no error; the closed-DB probe cannot detect a query attempt")
	}
	if _, err := store.CountAppsInSlugs([]string{"irrelevant"}); err == nil {
		t.Fatal("CountAppsInSlugs with a non-empty allowlist on a closed store returned no error; the closed-DB probe cannot detect a query attempt")
	}
	if _, err := store.ListAppsVisibleToUserInSlugs(owner.ID, []string{"irrelevant"}, 0, 0); err == nil {
		t.Fatal("ListAppsVisibleToUserInSlugs with a non-empty allowlist on a closed store returned no error; the closed-DB probe cannot detect a query attempt")
	}
	if _, err := store.CountAppsVisibleToUserInSlugs(owner.ID, []string{"irrelevant"}); err == nil {
		t.Fatal("CountAppsVisibleToUserInSlugs with a non-empty allowlist on a closed store returned no error; the closed-DB probe cannot detect a query attempt")
	}

	// The actual claim: an empty (nil, and separately empty-but-non-nil)
	// allowlist succeeds with no rows and no count, even against the closed
	// store, because it never reaches the database.
	for _, allow := range [][]string{nil, {}} {
		apps, err := store.ListAppsInSlugs(allow, 0, 0)
		if err != nil {
			t.Fatalf("ListAppsInSlugs(%#v) on closed store: %v (should short-circuit before touching the database)", allow, err)
		}
		if len(apps) != 0 {
			t.Fatalf("ListAppsInSlugs(%#v) = %d apps, want 0", allow, len(apps))
		}
		n, err := store.CountAppsInSlugs(allow)
		if err != nil {
			t.Fatalf("CountAppsInSlugs(%#v) on closed store: %v", allow, err)
		}
		if n != 0 {
			t.Fatalf("CountAppsInSlugs(%#v) = %d, want 0", allow, n)
		}

		vapps, err := store.ListAppsVisibleToUserInSlugs(owner.ID, allow, 0, 0)
		if err != nil {
			t.Fatalf("ListAppsVisibleToUserInSlugs(%#v) on closed store: %v", allow, err)
		}
		if len(vapps) != 0 {
			t.Fatalf("ListAppsVisibleToUserInSlugs(%#v) = %d apps, want 0", allow, len(vapps))
		}
		vn, err := store.CountAppsVisibleToUserInSlugs(owner.ID, allow)
		if err != nil {
			t.Fatalf("CountAppsVisibleToUserInSlugs(%#v) on closed store: %v", allow, err)
		}
		if vn != 0 {
			t.Fatalf("CountAppsVisibleToUserInSlugs(%#v) = %d, want 0", allow, vn)
		}
	}
}

// TestListAppsInSlugs_LargeAllowlist_BeyondBindVariableLimit proves the
// allowlist is bound as a single parameter regardless of length: an allowlist
// far larger than SQLite's per-statement bind-variable ceiling (32766 by
// default) must still succeed and return exactly the matching rows. A naive
// "one ? placeholder per slug" implementation (the same shape GetAppsBySlugs
// uses via inPlaceholders) would fail this with a SQLite bind-variable-limit
// error; this is the mutation-check target for that wrong fix.
func TestListAppsInSlugs_LargeAllowlist_BeyondBindVariableLimit(t *testing.T) {
	store := dbtest.New(t)
	owner := mustCreateUser(t, store, "owner", "developer")

	const n = 40000
	allow := make([]string, n)
	for i := range allow {
		allow[i] = fmt.Sprintf("noise-%d", i)
	}
	// Plant real apps at the start, middle, and end of the allowlist so the
	// large-allowlist path is checked for correctness, not just for surviving
	// without an error.
	planted := []int{0, n / 2, n - 1}
	var plantedSlugs []string
	for _, idx := range planted {
		slug := fmt.Sprintf("planted-%d", idx)
		allow[idx] = slug
		plantedSlugs = append(plantedSlugs, slug)
		mustCreateApp(t, store, slug, owner.ID)
	}
	sort.Strings(plantedSlugs)

	apps, err := store.ListAppsInSlugs(allow, 0, 0)
	if err != nil {
		t.Fatalf("ListAppsInSlugs with a %d-entry allowlist: %v (a single-parameter allowlist must not hit the bind-variable limit)", n, err)
	}
	if got := scopedSlugsOf(apps); !sameSlugs(got, plantedSlugs) {
		t.Fatalf("ListAppsInSlugs with a %d-entry allowlist = %v, want %v", n, got, plantedSlugs)
	}

	count, err := store.CountAppsInSlugs(allow)
	if err != nil {
		t.Fatalf("CountAppsInSlugs with a %d-entry allowlist: %v", n, err)
	}
	if count != len(plantedSlugs) {
		t.Fatalf("CountAppsInSlugs with a %d-entry allowlist = %d, want %d", n, count, len(plantedSlugs))
	}
}

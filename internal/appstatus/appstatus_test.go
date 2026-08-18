package appstatus

import "testing"

// Every observed status must be classified. A new status that lands in
// Observed without a Class case makes this fail, which is the point: the
// v0.11.14 `idle` status shipped with no client that knew it, so every
// readiness gate waited on it until it timed out.
func TestClass_CoversEveryObservedStatus(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range Observed {
		if seen[s] {
			t.Errorf("Observed lists %q twice", s)
		}
		seen[s] = true
		if Class(s) == KindUnknown {
			t.Errorf("Class(%q) = Unknown; every observed status needs a class", s)
		}
	}
	if Class("no-such-status") != KindUnknown {
		t.Errorf("Class of an unrecognised word must be Unknown")
	}
	if Class("") != KindUnknown {
		t.Errorf("Class of the empty string must be Unknown")
	}
}

func TestServing(t *testing.T) {
	want := map[string]bool{
		Running: true, Idle: true,
		Starting: false, Degraded: false, Crashed: false, Hibernated: false,
		Suspended: false, Stopped: false, Deleting: false, Waking: false,
		Deploying: false, "": false, "bogus": false,
	}
	for _, s := range Observed {
		if _, ok := want[s]; !ok {
			t.Fatalf("test table lacks an expectation for observed status %q", s)
		}
	}
	for status, exp := range want {
		if got := Serving(status); got != exp {
			t.Errorf("Serving(%q) = %v, want %v", status, got, exp)
		}
	}
}

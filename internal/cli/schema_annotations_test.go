package cli

import "testing"

// TestAnnotations_KnownCommandShape spot-checks representative entries so the
// registry types stay honest before the full-tree coverage test (Task 10).
func TestAnnotations_KnownCommandShape(t *testing.T) {
	a, ok := schemaAnnotations["apps list"]
	if !ok {
		t.Fatal("missing annotation for `apps list`")
	}
	if a.Mutating == nil || *a.Mutating != false {
		t.Error("apps list must be explicitly mutating=false")
	}
	if len(a.OutputFields) == 0 {
		t.Error("apps list must declare output_fields")
	}
	d, ok := schemaAnnotations["apps delete"]
	if !ok || d.Mutating == nil || !*d.Mutating {
		t.Error("apps delete must be explicitly mutating=true")
	}
}

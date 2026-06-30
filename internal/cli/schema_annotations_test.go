package cli

import (
	"strings"
	"testing"
)

// TestAnnotations_KnownCommandShape spot-checks representative entries so the
// registry types stay honest. Full-tree coverage is enforced by the
// conformance tests in cmd/shinyhub.
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

func TestFleetApplyAnnotation_DocumentsFailureKind(t *testing.T) {
	ann, ok := schemaAnnotations["fleet apply"]
	if !ok {
		t.Fatal("fleet apply must have a schema annotation")
	}
	var hasKind, hasDetails bool
	for _, f := range ann.OutputFields {
		switch f.Name {
		case "failure_kind":
			hasKind = true
		case "attempt_details":
			hasDetails = true
		}
	}
	if !hasKind || !hasDetails {
		t.Fatalf("fleet apply OutputFields must document failure_kind and attempt_details, got %+v", ann.OutputFields)
	}
}

func TestDeployAnnotation_DocumentsBuildTimeout(t *testing.T) {
	ann, ok := schemaAnnotations["deploy"]
	if !ok {
		t.Fatal("deploy must have a schema annotation")
	}
	if !strings.Contains(ann.Notes, "build_timeout_seconds") {
		t.Fatalf("deploy Notes must document build_timeout_seconds, got %q", ann.Notes)
	}
}

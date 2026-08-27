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
			hasKind = strings.Contains(f.Desc, failureWarmNeverSucceeded) && strings.Contains(f.Desc, failureScheduleStale)
		case "attempt_details":
			hasDetails = true
		}
	}
	if !hasKind || !hasDetails {
		t.Fatalf("fleet apply OutputFields must document deploy and schedule gate failure kinds plus attempt_details, got %+v", ann.OutputFields)
	}
}

func TestScheduleAnnotations_DocumentAtomicFreshnessState(t *testing.T) {
	for _, command := range []string{"schedule ls", "schedule status"} {
		ann := schemaAnnotations[command]
		fields := map[string]bool{}
		for _, f := range ann.OutputFields {
			fields[f.Name] = true
		}
		for _, name := range []string{"last_run_id", "stale", "refreshing", "active_run_id", "freshness_error"} {
			if !fields[name] {
				t.Errorf("%s must document %s, got %+v", command, name, ann.OutputFields)
			}
		}
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
	if !ann.Streaming || !strings.Contains(ann.Notes, "--output ndjson") || !strings.Contains(ann.Notes, "error event") {
		t.Fatalf("deploy annotation must document its optional event stream, got %+v", ann)
	}
	for _, want := range []string{"--watch", "explicit --host", "--allow-repeated-hooks", "several attempt results"} {
		if !strings.Contains(ann.Notes, want) {
			t.Fatalf("deploy annotation must document watch contract %q, got %q", want, ann.Notes)
		}
	}
}

func TestFleetApplyAnnotation_DocumentsConcurrency(t *testing.T) {
	ann, ok := schemaAnnotations["fleet apply"]
	if !ok {
		t.Fatal("fleet apply must have a schema annotation")
	}
	if !strings.Contains(ann.Notes, "concurrency") {
		t.Fatalf("fleet apply Notes must mention concurrency, got %q", ann.Notes)
	}
}

func TestFleetDevAnnotation_IsReadOnlyStreamingAndPathAware(t *testing.T) {
	ann, ok := schemaAnnotations["fleet dev"]
	if !ok {
		t.Fatal("fleet dev must have a schema annotation")
	}
	if ann.Mutating == nil || *ann.Mutating {
		t.Fatal("fleet dev must be read-only with respect to ShinyHub server state")
	}
	if !ann.Streaming {
		t.Fatal("fleet dev must document streaming process output")
	}
	for _, flag := range []string{"--file", "--data-dir", "--env-file", "--state-dir"} {
		if ann.ArgTypes[flag] != "path" {
			t.Errorf("fleet dev %s schema type = %q, want path", flag, ann.ArgTypes[flag])
		}
	}
}

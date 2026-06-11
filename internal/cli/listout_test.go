package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func sampleItems() []map[string]any {
	return []map[string]any{
		{"slug": "a", "status": "running", "deploy_count": 3},
		{"slug": "b", "status": "stopped", "deploy_count": 1},
		{"slug": "c", "status": "running", "deploy_count": 7},
	}
}

func TestRenderList_JSONEnvelope(t *testing.T) {
	var out, errBuf bytes.Buffer
	f := &listFlags{limit: 2, offset: 1}
	err := renderListTo(&out, &errBuf, formatJSON, f, sampleItems(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Items  []map[string]any `json:"items"`
		Total  int              `json:"total"`
		Limit  int              `json:"limit"`
		Offset int              `json:"offset"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Total != 3 || env.Limit != 2 || env.Offset != 1 || len(env.Items) != 2 {
		t.Errorf("envelope = %+v", env)
	}
	if env.Items[0]["slug"] != "b" {
		t.Errorf("offset not applied: first item %v", env.Items[0])
	}
}

func TestRenderList_FieldsProjection(t *testing.T) {
	var out, errBuf bytes.Buffer
	f := &listFlags{fields: "slug,status"}
	if err := renderListTo(&out, &errBuf, formatJSON, f, sampleItems(), nil, nil); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out.Bytes())
	}
	if _, has := env.Items[0]["deploy_count"]; has {
		t.Error("--fields did not project away deploy_count")
	}
	// Requested fields must be present in the projected items.
	if _, has := env.Items[0]["slug"]; !has {
		t.Error("--fields projected away slug which was requested")
	}
	if _, has := env.Items[0]["status"]; !has {
		t.Error("--fields projected away status which was requested")
	}
}

func TestRenderList_UnknownFieldIsValidationError(t *testing.T) {
	var out, errBuf bytes.Buffer
	f := &listFlags{fields: "slug,bogus"}
	err := renderListTo(&out, &errBuf, formatJSON, f, sampleItems(), nil, nil)
	var ece *ExitCodeError
	if err == nil || !errors.As(err, &ece) || ece.Kind != KindValidation {
		t.Fatalf("want validation error listing valid fields, got %v", err)
	}
	if !strings.Contains(err.Error(), "slug") {
		t.Errorf("error should list valid fields: %v", err)
	}
}

func TestRenderList_ExtraEnvelopeKeysPreserved(t *testing.T) {
	var out, errBuf bytes.Buffer
	f := &listFlags{}
	extra := map[string]any{"quota_mb": 512, "used_bytes": 1024}
	if err := renderListTo(&out, &errBuf, formatJSON, f, sampleItems(), extra, nil); err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out.Bytes())
	}
	if env["quota_mb"] != float64(512) {
		t.Errorf("extra envelope key lost: %v", env)
	}
	if env["used_bytes"] != float64(1024) {
		t.Errorf("extra envelope key lost: %v", env)
	}
	// Standard envelope keys must still hold their expected values after the merge.
	if env["total"] != float64(3) {
		t.Errorf("total corrupted by extra-key merge: %v", env["total"])
	}
	if env["limit"] != float64(0) {
		t.Errorf("limit corrupted by extra-key merge: %v", env["limit"])
	}
	if env["offset"] != float64(0) {
		t.Errorf("offset corrupted by extra-key merge: %v", env["offset"])
	}
	if items, ok := env["items"].([]any); !ok || len(items) != 3 {
		t.Errorf("items corrupted by extra-key merge: %v", env["items"])
	}
}

// TestSliceAndProject_NegativeOffsetValidationError verifies that a negative
// --offset is rejected with a KindValidation error rather than causing a
// negative-slice-index panic.
func TestSliceAndProject_NegativeOffsetValidationError(t *testing.T) {
	f := &listFlags{offset: -1}
	_, err := sliceAndProject(sampleItems(), f)
	if err == nil {
		t.Fatal("want validation error for negative offset, got nil")
	}
	var ece *ExitCodeError
	if !errors.As(err, &ece) || ece.Kind != KindValidation {
		t.Errorf("want KindValidation, got %v", err)
	}
	if !strings.Contains(err.Error(), "offset") {
		t.Errorf("error should name the --offset flag, got: %v", err)
	}
}

// TestSliceAndProject_NegativeLimitValidationError verifies that a negative
// --limit is rejected with a KindValidation error rather than silently passing
// through (f.limit > 0 check treats -1 as "no limit" which is wrong).
func TestSliceAndProject_NegativeLimitValidationError(t *testing.T) {
	f := &listFlags{limit: -1}
	_, err := sliceAndProject(sampleItems(), f)
	if err == nil {
		t.Fatal("want validation error for negative limit, got nil")
	}
	var ece *ExitCodeError
	if !errors.As(err, &ece) || ece.Kind != KindValidation {
		t.Errorf("want KindValidation, got %v", err)
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error should name the --limit flag, got: %v", err)
	}
}

// TestSliceAndProject_ZeroOffsetAndLimitAreValid verifies that 0 values for
// both flags are treated as "no bound" (the documented default) and don't
// error. This ensures the negative check doesn't accidentally reject 0.
func TestSliceAndProject_ZeroOffsetAndLimitAreValid(t *testing.T) {
	items := sampleItems()
	f := &listFlags{offset: 0, limit: 0}
	got, err := sliceAndProject(items, f)
	if err != nil {
		t.Fatalf("offset=0 limit=0 should succeed, got error: %v", err)
	}
	if len(got) != len(items) {
		t.Errorf("got %d items, want %d (no bound applied)", len(got), len(items))
	}
}

// TestSliceAndProject_EmptyItemsWithFieldsSkipsValidation verifies that when
// the items list is empty, --fields does not error on "unknown fields" (there
// is no schema to validate against; empty is empty). This allows paginated
// responses on the last page to use --fields without error.
func TestSliceAndProject_EmptyItemsWithFieldsSkipsValidation(t *testing.T) {
	f := &listFlags{fields: "slug,status"}
	got, err := sliceAndProject([]map[string]any{}, f)
	if err != nil {
		t.Fatalf("empty items + --fields should succeed, got: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty result, got %d items", len(got))
	}
}

func TestRenderList_TableTruncationNotice(t *testing.T) {
	var out, errBuf bytes.Buffer
	f := &listFlags{limit: 1}
	tableFn := func(w io.Writer, items []map[string]any) {
		for _, it := range items {
			fmt.Fprintln(w, it["slug"])
		}
	}
	if err := renderListTo(&out, &errBuf, formatTable, f, sampleItems(), nil, tableFn); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errBuf.String(), "showing 1 of 3") {
		t.Errorf("truncation notice missing from stderr: %q", errBuf.String())
	}
}

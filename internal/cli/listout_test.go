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
	_ = json.Unmarshal(out.Bytes(), &env)
	if _, has := env.Items[0]["deploy_count"]; has {
		t.Error("--fields did not project away deploy_count")
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
	_ = json.Unmarshal(out.Bytes(), &env)
	if env["quota_mb"] != float64(512) {
		t.Errorf("extra envelope key lost: %v", env)
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

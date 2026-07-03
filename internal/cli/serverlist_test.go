package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

// serverPage simulates a page the server already sliced: two items out of a
// larger total.
func serverPage() []map[string]any {
	return []map[string]any{
		{"slug": "b", "status": "stopped", "deploy_count": 1},
		{"slug": "c", "status": "running", "deploy_count": 7},
	}
}

// TestRenderServerList_JSONEnvelopeTrustsServer verifies the helper echoes the
// server-provided page + total verbatim (no client-side re-slicing) and reports
// the limit/offset the CLI sent.
func TestRenderServerList_JSONEnvelopeTrustsServer(t *testing.T) {
	var out, errBuf bytes.Buffer
	f := &listFlags{limit: 2, offset: 1}
	err := renderServerListTo(&out, &errBuf, formatJSON, f, serverPage(), 5, nil, nil)
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
	// total comes from the server (5), NOT len(page)=2.
	if env.Total != 5 {
		t.Errorf("total = %d, want 5 (server total, not page size)", env.Total)
	}
	if env.Limit != 2 || env.Offset != 1 {
		t.Errorf("limit/offset = %d/%d, want 2/1", env.Limit, env.Offset)
	}
	// The page is rendered as-is (server already applied offset).
	if len(env.Items) != 2 || env.Items[0]["slug"] != "b" {
		t.Errorf("page not rendered verbatim: %+v", env.Items)
	}
}

// TestRenderServerList_FieldsProjection verifies --fields still projects the
// returned page client-side.
func TestRenderServerList_FieldsProjection(t *testing.T) {
	var out, errBuf bytes.Buffer
	f := &listFlags{fields: "slug"}
	if err := renderServerListTo(&out, &errBuf, formatJSON, f, serverPage(), 5, nil, nil); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if _, has := env.Items[0]["deploy_count"]; has {
		t.Error("--fields did not project away deploy_count")
	}
	if _, has := env.Items[0]["slug"]; !has {
		t.Error("--fields projected away slug which was requested")
	}
}

// TestRenderServerList_TruncationNoticeUsesServerTotal verifies the "showing X
// of Y" hint reflects the server total, not the page length.
func TestRenderServerList_TruncationNoticeUsesServerTotal(t *testing.T) {
	var out, errBuf bytes.Buffer
	f := &listFlags{limit: 2, offset: 0}
	tableFn := func(w io.Writer, items []map[string]any) {
		for _, it := range items {
			fmt.Fprintln(w, it["slug"])
		}
	}
	if err := renderServerListTo(&out, &errBuf, formatTable, f, serverPage(), 5, nil, tableFn); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errBuf.String(), "showing 2 of 5") {
		t.Errorf("truncation notice wrong: %q", errBuf.String())
	}
}

// TestRenderServerList_NoNoticeWhenPageIsWhole verifies no hint when the page
// already covers the whole result set.
func TestRenderServerList_NoNoticeWhenPageIsWhole(t *testing.T) {
	var out, errBuf bytes.Buffer
	f := &listFlags{}
	tableFn := func(w io.Writer, items []map[string]any) {}
	if err := renderServerListTo(&out, &errBuf, formatTable, f, serverPage(), 2, nil, tableFn); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errBuf.String(), "showing") {
		t.Errorf("unexpected truncation notice when page == total: %q", errBuf.String())
	}
}

// TestRenderServerList_ExtraEnvelopeKeys verifies command-specific envelope keys
// survive and never clobber the standard fields.
func TestRenderServerList_ExtraEnvelopeKeys(t *testing.T) {
	var out, errBuf bytes.Buffer
	f := &listFlags{}
	extra := map[string]any{"quota_mb": 512, "total": 999}
	if err := renderServerListTo(&out, &errBuf, formatJSON, f, serverPage(), 2, extra, nil); err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env["quota_mb"] != float64(512) {
		t.Errorf("extra key lost: %v", env)
	}
	// A colliding "total" in extra must not override the authoritative total.
	if env["total"] != float64(2) {
		t.Errorf("extra clobbered total: %v", env["total"])
	}
}

// TestRenderServerList_EmptyPageMarshalsArray verifies an empty page renders as
// [] not null.
func TestRenderServerList_EmptyPageMarshalsArray(t *testing.T) {
	var out, errBuf bytes.Buffer
	f := &listFlags{}
	if err := renderServerListTo(&out, &errBuf, formatJSON, f, nil, 0, nil, nil); err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if _, ok := env["items"].([]any); !ok {
		t.Errorf("items must be [] not null: %T %v", env["items"], env["items"])
	}
}

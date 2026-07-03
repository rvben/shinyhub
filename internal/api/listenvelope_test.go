package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// decodeEnvelope reads the recorder body into the standard list envelope shape.
func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode envelope: %v (body=%q)", err, rec.Body.String())
	}
	return env
}

func TestWriteList_FullNoPagination(t *testing.T) {
	rec := httptest.NewRecorder()
	writeList(rec, []int{10, 20, 30}, 0, 0, nil)

	env := decodeEnvelope(t, rec)
	items, ok := env["items"].([]any)
	if !ok {
		t.Fatalf("items not an array: %T", env["items"])
	}
	if len(items) != 3 {
		t.Fatalf("items len = %d, want 3", len(items))
	}
	if env["total"] != float64(3) {
		t.Errorf("total = %v, want 3", env["total"])
	}
	if env["limit"] != float64(0) {
		t.Errorf("limit = %v, want 0", env["limit"])
	}
	if env["offset"] != float64(0) {
		t.Errorf("offset = %v, want 0", env["offset"])
	}
}

func TestWriteList_SlicesByLimitOffset(t *testing.T) {
	rec := httptest.NewRecorder()
	writeList(rec, []int{1, 2, 3, 4, 5}, 2, 1, nil)

	env := decodeEnvelope(t, rec)
	items := env["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("page len = %d, want 2", len(items))
	}
	if items[0] != float64(2) || items[1] != float64(3) {
		t.Errorf("page = %v, want [2 3]", items)
	}
	// total is the full count, not the page size.
	if env["total"] != float64(5) {
		t.Errorf("total = %v, want 5", env["total"])
	}
	if env["limit"] != float64(2) {
		t.Errorf("limit = %v, want 2", env["limit"])
	}
	if env["offset"] != float64(1) {
		t.Errorf("offset = %v, want 1", env["offset"])
	}
}

func TestWriteList_EmptyMarshalsArrayNotNull(t *testing.T) {
	rec := httptest.NewRecorder()
	writeList(rec, []int{}, 0, 0, nil)

	// The raw body must carry an empty JSON array, never null.
	got := rec.Body.String()
	want := `{"items":[],"limit":0,"offset":0,"total":0}` + "\n"
	if got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestWriteList_NilSliceMarshalsArrayNotNull(t *testing.T) {
	rec := httptest.NewRecorder()
	var items []int // nil
	writeList(rec, items, 0, 0, nil)

	env := decodeEnvelope(t, rec)
	if _, ok := env["items"].([]any); !ok {
		t.Fatalf("items must be [] not null; got %T (%v)", env["items"], env["items"])
	}
	if env["total"] != float64(0) {
		t.Errorf("total = %v, want 0", env["total"])
	}
}

func TestWriteList_OffsetBeyondLenIsEmptyPage(t *testing.T) {
	rec := httptest.NewRecorder()
	writeList(rec, []int{1, 2, 3}, 0, 99, nil)

	env := decodeEnvelope(t, rec)
	items := env["items"].([]any)
	if len(items) != 0 {
		t.Errorf("page = %v, want empty", items)
	}
	// total still reflects the full set.
	if env["total"] != float64(3) {
		t.Errorf("total = %v, want 3", env["total"])
	}
}

func TestWriteList_ExtraEnvelopeKeys(t *testing.T) {
	rec := httptest.NewRecorder()
	writeList(rec, []int{1}, 0, 0, map[string]any{"quota_mb": 100, "used_bytes": 42})

	env := decodeEnvelope(t, rec)
	if env["quota_mb"] != float64(100) {
		t.Errorf("quota_mb = %v, want 100", env["quota_mb"])
	}
	if env["used_bytes"] != float64(42) {
		t.Errorf("used_bytes = %v, want 42", env["used_bytes"])
	}
	// Extra keys never clobber the standard envelope fields.
	if env["total"] != float64(1) {
		t.Errorf("total = %v, want 1", env["total"])
	}
}

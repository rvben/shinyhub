package cli

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGetPaginatedListWithExtra_ReturnsEnvelopeExtras verifies the fetch helper
// surfaces command-specific envelope keys (e.g. data ls quota_mb/used_bytes)
// while excluding the standard fields from the extra map.
func TestGetPaginatedListWithExtra_ReturnsEnvelopeExtras(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"items":[{"path":"a.csv"}],"total":3,"limit":0,"offset":0,"quota_mb":100,"used_bytes":42}`))
	}))
	defer srv.Close()
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}

	items, total, extra, err := getPaginatedListWithExtra(cfg, "list data", "/api/x", &listFlags{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0]["path"] != "a.csv" {
		t.Errorf("items = %v", items)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if extra["quota_mb"] != float64(100) || extra["used_bytes"] != float64(42) {
		t.Errorf("extra = %v, want quota_mb=100 used_bytes=42", extra)
	}
	// Standard envelope fields must not leak into extra.
	for _, k := range []string{"items", "total", "limit", "offset"} {
		if _, ok := extra[k]; ok {
			t.Errorf("extra must not contain standard field %q", k)
		}
	}
}

// TestGetPaginatedList_NegativeOffsetValidation verifies a negative --offset is
// rejected as a validation error before any HTTP request is made. This mirrors
// the pre-existing client-side sliceAndProject check so the switch to
// server-side pagination does not silently drop input validation.
func TestGetPaginatedList_NegativeOffsetValidation(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}

	_, _, err := getPaginatedList(cfg, "list x", "/api/x", &listFlags{offset: -1})
	var ece *ExitCodeError
	if err == nil || !errors.As(err, &ece) || ece.Kind != KindValidation {
		t.Fatalf("want KindValidation error for negative --offset, got %v", err)
	}
	if called {
		t.Error("negative --offset must fail before any HTTP request is made")
	}
}

// TestGetPaginatedList_NegativeLimitValidation verifies a negative --limit is
// rejected as a validation error before any HTTP request is made.
func TestGetPaginatedList_NegativeLimitValidation(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}

	_, _, err := getPaginatedList(cfg, "list x", "/api/x", &listFlags{limit: -5})
	var ece *ExitCodeError
	if err == nil || !errors.As(err, &ece) || ece.Kind != KindValidation {
		t.Fatalf("want KindValidation error for negative --limit, got %v", err)
	}
	if called {
		t.Error("negative --limit must fail before any HTTP request is made")
	}
}

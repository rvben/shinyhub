package cli

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

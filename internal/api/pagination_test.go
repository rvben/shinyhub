package api

import (
	"net/http/httptest"
	"testing"
)

// TestParsePagination_ClampsExcessiveLimit proves a caller cannot force an
// unbounded response by supplying an arbitrarily large ?limit=. Every handler
// that reads pagination through parsePagination inherits this cap.
func TestParsePagination_ClampsExcessiveLimit(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/audit?limit=999999999", nil)
	limit, _ := parsePagination(req)
	if limit != maxPaginationLimit {
		t.Fatalf("limit = %d, want clamped to %d", limit, maxPaginationLimit)
	}
}

// TestParsePagination_AbsentLimitStaysUnbounded pins the existing "no limit
// param means no bound" contract several callers rely on (e.g. listing every
// app in one response for a small fleet); only an explicit excessive value is
// clamped.
func TestParsePagination_AbsentLimitStaysUnbounded(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/audit", nil)
	limit, offset := parsePagination(req)
	if limit != 0 || offset != 0 {
		t.Fatalf("limit=%d offset=%d, want 0,0 for an absent limit", limit, offset)
	}
}

// TestParsePagination_WithinBoundsPassesThrough proves an ordinary limit and
// offset are unaffected by the clamp.
func TestParsePagination_WithinBoundsPassesThrough(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/audit?limit=25&offset=10", nil)
	limit, offset := parsePagination(req)
	if limit != 25 || offset != 10 {
		t.Fatalf("limit=%d offset=%d, want 25,10", limit, offset)
	}
}

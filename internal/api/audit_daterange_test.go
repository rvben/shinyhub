package api

import (
	"testing"
	"time"
)

func TestParseAuditDateRange_BoundsAreInclusiveUTCDays(t *testing.T) {
	since, until, err := parseAuditDateRange("2026-09-01", "2026-09-06")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if want := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC); !since.Equal(want) {
		t.Errorf("since = %s, want %s", since, want)
	}
	// The end bound must cover the whole named day. An operator asking for
	// events until the 6th means through the end of the 6th; a midnight bound
	// silently drops everything that happened during it.
	if want := time.Date(2026, 9, 6, 23, 59, 59, 0, time.UTC); !until.Equal(want) {
		t.Errorf("until = %s, want %s", until, want)
	}
}

func TestParseAuditDateRange_EachEndIsOptional(t *testing.T) {
	since, until, err := parseAuditDateRange("2026-09-01", "")
	if err != nil {
		t.Fatalf("since-only: %v", err)
	}
	if since.IsZero() {
		t.Error("since-only range dropped the since bound")
	}
	if !until.IsZero() {
		t.Errorf("since-only range invented an until bound: %s", until)
	}

	since, until, err = parseAuditDateRange("", "2026-09-06")
	if err != nil {
		t.Fatalf("until-only: %v", err)
	}
	if !since.IsZero() {
		t.Errorf("until-only range invented a since bound: %s", since)
	}
	if until.IsZero() {
		t.Error("until-only range dropped the until bound")
	}

	if since, until, err = parseAuditDateRange("", ""); err != nil || !since.IsZero() || !until.IsZero() {
		t.Errorf("empty range = (%s, %s, %v), want both zero and no error", since, until, err)
	}
}

// TestParseAuditDateRange_RejectsRatherThanIgnores is the security-relevant
// half. Dropping a bound the caller asked for returns MORE rows than requested,
// which reads as "those events fall in the window" - the wrong answer to give
// someone reading an audit log.
func TestParseAuditDateRange_RejectsRatherThanIgnores(t *testing.T) {
	for _, tc := range []struct{ name, since, until string }{
		{"unparseable since", "last-tuesday", ""},
		{"unpadded since", "2026-9-1", ""},
		{"unparseable until", "", "soon"},
		{"timestamp not a date", "2026-09-01T10:00:00Z", ""},
		{"inverted range", "2026-09-06", "2026-09-01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := parseAuditDateRange(tc.since, tc.until); err == nil {
				t.Errorf("parseAuditDateRange(%q, %q) accepted an input it cannot honour", tc.since, tc.until)
			}
		})
	}
}

// TestParseAuditDateRange_AcceptsAnEqualRange is the other bound on the
// inversion check: a single-day window is the commonest range an operator asks
// for and must not be rejected as inverted.
func TestParseAuditDateRange_AcceptsAnEqualRange(t *testing.T) {
	since, until, err := parseAuditDateRange("2026-09-06", "2026-09-06")
	if err != nil {
		t.Fatalf("single-day range rejected: %v", err)
	}
	if !until.After(since) {
		t.Errorf("single-day range collapsed: since %s, until %s", since, until)
	}
}

func TestParseAuditDateRange_TrimsSurroundingSpace(t *testing.T) {
	if _, _, err := parseAuditDateRange(" 2026-09-01 ", " 2026-09-06 "); err != nil {
		t.Errorf("a padded date was rejected: %v", err)
	}
}

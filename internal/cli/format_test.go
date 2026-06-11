package cli

import (
	"errors"
	"testing"
)

func TestResolveFormat(t *testing.T) {
	cases := []struct {
		name      string
		flagValue string // -o value, "" = unset
		legacy    bool   // --json given
		stdoutTTY bool
		streaming bool
		want      outputFormat
		wantErr   bool
	}{
		{"tty default table", "", false, true, false, formatTable, false},
		{"piped document json", "", false, false, false, formatJSON, false},
		{"piped streaming ndjson", "", false, false, true, formatNDJSON, false},
		{"explicit json wins on tty", "json", false, true, false, formatJSON, false},
		{"explicit table wins when piped", "table", false, false, false, formatTable, false},
		{"legacy json alias", "", true, true, false, formatJSON, false},
		{"legacy agrees with -o", "json", true, true, false, formatJSON, false},
		{"legacy conflicts with -o table", "table", true, true, false, "", true},
		{"json rejected for streaming", "json", false, false, true, "", true},
		{"ndjson rejected for document", "ndjson", false, false, false, "", true},
		{"unknown format", "yaml", false, true, false, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveFormatWith(tc.flagValue, tc.legacy, tc.stdoutTTY, tc.streaming)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				var ece *ExitCodeError
				if !errors.As(err, &ece) || ece.Kind != KindValidation {
					t.Errorf("format error must be kind validation: %v", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("got (%q, %v), want %q", got, err, tc.want)
			}
		})
	}
}

// resetFormatState clears package-level format state between tests; cobra
// persistent flags and the resolver cache leak across Execute calls.
func resetFormatState(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		outputFlagValue = ""
		quietFlag = false
		resolvedFormat = ""
	})
}

// TestCurrentFormat_ExplicitOutputFlagBeforeTTYFallback verifies that when
// outputFlagValue holds a valid format string and resolvedFormat is empty
// (error occurred before command resolution), currentFormat returns the
// explicit value rather than falling through to TTY detection. This ensures
// -o json causes JSON error envelopes even when stdout is a TTY.
func TestCurrentFormat_ExplicitOutputFlagBeforeTTYFallback(t *testing.T) {
	resetFormatState(t)

	outputFlagValue = "json"
	// resolvedFormat stays "" (simulating an error before resolveFormat ran)
	got := currentFormat()
	if got != formatJSON {
		t.Errorf("currentFormat() = %q, want json when -o json is set", got)
	}
}

// TestCurrentFormat_InvalidOutputFlagFallsBackToTTY verifies that an invalid
// -o value does NOT propagate through currentFormat (currentFormat must return
// something valid for the TTY path; the invalid value will be rejected later
// by resolveFormat). When both resolvedFormat and outputFlagValue are invalid,
// we fall back to TTY detection. Use resolvedFormat="" and an invalid flag
// value, then confirm currentFormat returns formatJSON (stdout not a TTY).
func TestCurrentFormat_InvalidOutputFlagFallsBackToTTYPath(t *testing.T) {
	resetFormatState(t)

	outputFlagValue = "bogus"
	// resolvedFormat stays "" and stdout is not a TTY in tests.
	got := currentFormat()
	// Expect TTY fallback: not a TTY in CI/test => formatJSON
	if got != formatJSON && got != formatTable {
		t.Errorf("currentFormat() = %q, want table or json (TTY-based), not the invalid flag value", got)
	}
	// The invalid value must NOT appear as the returned format.
	if got == outputFormat("bogus") {
		t.Errorf("currentFormat() returned the invalid flag value %q", got)
	}
}

// TestCurrentFormat_ResolvedFormatWinsOverExplicitFlag verifies the priority
// order: resolvedFormat (set by a successful resolveFormat call) always wins
// over outputFlagValue, even when outputFlagValue differs. This is the
// normal success path: a command's resolveFormat runs and caches the result.
func TestCurrentFormat_ResolvedFormatWinsOverExplicitFlag(t *testing.T) {
	resetFormatState(t)

	resolvedFormat = formatNDJSON
	outputFlagValue = "json" // would conflict, but resolvedFormat takes priority
	got := currentFormat()
	if got != formatNDJSON {
		t.Errorf("currentFormat() = %q, want ndjson (resolvedFormat priority)", got)
	}
}

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

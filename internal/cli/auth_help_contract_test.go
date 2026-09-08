package cli

import (
	"strings"
	"testing"
)

// connect and login both authenticate and both save a credential, so a reader
// meeting the command list for the first time has no way to choose between
// them from the names alone. Each help text now says which to prefer and why.
// Both directions are pinned: a reader who found only one of the two commands
// still has to be told the other exists, so a cross-reference that survives in
// one help text and is dropped from the other is a half-fix.
func TestConnectAndLoginHelpReferenceEachOther(t *testing.T) {
	cases := []struct {
		cmd  string
		long string
		// other is the sibling command the text must name, and phrase is a
		// distinctive fragment of the guidance itself. Naming the sibling alone
		// would pass on an incidental mention (connect's --refresh prose could
		// come to say "login" for unrelated reasons), so both are required.
		other  string
		phrase string
	}{
		{"connect", newConnectCmd().Long, "login", "Prefer connect"},
		{"login", newLoginCmd().Long, "connect", "usually the one to reach for"},
	}

	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			// Help text is hard-wrapped, so a phrase that reads as one line to a
			// person can carry a newline mid-sentence. Match against the
			// whitespace-collapsed form or a future rewrap breaks this test
			// without anything being wrong.
			tc.long = strings.Join(strings.Fields(tc.long), " ")
			if !strings.Contains(tc.long, tc.other) {
				t.Errorf("`%s --help` never mentions `%s`, so a reader cannot tell the two apart", tc.cmd, tc.other)
			}
			if !strings.Contains(tc.long, tc.phrase) {
				t.Errorf("`%s --help` no longer carries the guidance %q; naming the sibling without saying which to use answers nothing", tc.cmd, tc.phrase)
			}
		})
	}
}

// The help text of a command a person reads at a terminal is public-facing
// output, and this project writes no em or en dashes in any of it.
func TestAuthCommandHelpUsesNoLongDashes(t *testing.T) {
	for _, c := range []struct {
		name string
		text string
	}{
		{"connect", newConnectCmd().Long},
		{"login", newLoginCmd().Long},
	} {
		for _, dash := range []string{"—", "–"} {
			if strings.Contains(c.text, dash) {
				t.Errorf("`%s --help` contains %q; use a hyphen, a comma, or parentheses", c.name, dash)
			}
		}
	}
}

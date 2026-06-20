package cli

import (
	"testing"
)

func TestRunCmd_FlagsAndNoLogin(t *testing.T) {
	cmd := newRunCmd()
	if cmd.Use[:3] != "run" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	for _, f := range []string{"port", "no-sync", "no-reload", "env", "env-file", "data-dir", "slug", "open", "check"} {
		if cmd.Flags().Lookup(f) == nil {
			t.Errorf("missing flag --%s", f)
		}
	}
}

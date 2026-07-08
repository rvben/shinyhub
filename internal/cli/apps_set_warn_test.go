package cli

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// setWith runs `apps set --isolation per_session` against a stub server that
// responds 200, optionally attaching the X-ShinyHub-Warning header the real
// server sends when elastic isolation is configured with no memory guard.
func setWith(t *testing.T, warning string) (out, errb string) {
	t.Helper()
	setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if warning != "" {
			w.Header().Set("X-ShinyHub-Warning", warning)
		}
		w.WriteHeader(http.StatusOK)
	})
	cmd := newAppsCmd()
	var o, e bytes.Buffer
	cmd.SetOut(&o)
	cmd.SetErr(&e)
	cmd.SetArgs([]string{"set", "demo", "--isolation", "per_session", "--max-workers", "2"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return o.String(), e.String()
}

// A guarded server sends no warning; the CLI must stay silent.
func TestAppsSet_SilentWithoutServerWarning(t *testing.T) {
	_, errb := setWith(t, "")
	if strings.Contains(errb, "warning:") {
		t.Errorf("expected no warning output, got %q", errb)
	}
}

// When the server warns that elastic isolation has no memory guard, the CLI
// relays it on stderr, matching the access-grant warning relay.
func TestAppsSet_RelaysServerWarning(t *testing.T) {
	_, errb := setWith(t, "per_session isolation has no memory guard")
	if !strings.Contains(errb, "warning: per_session isolation has no memory guard") {
		t.Errorf("expected the server warning on stderr, got %q", errb)
	}
}

package cli

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// grantWith runs `apps access grant` against a stub server that responds 204,
// optionally attaching the X-ShinyHub-Warning header the real server sends when
// the app's visibility already admits everyone (shared/public).
func grantWith(t *testing.T, warning string) (out, errb string) {
	t.Helper()
	setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if warning != "" {
			w.Header().Set("X-ShinyHub-Warning", warning)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	cmd := newAppsCmd()
	var o, e bytes.Buffer
	cmd.SetOut(&o)
	cmd.SetErr(&e)
	cmd.SetArgs([]string{"access", "grant", "demo", "alice"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return o.String(), e.String()
}

// A grant on a private app is exactly what admits the member, so the CLI must
// not second-guess it with a warning (the server sends none).
func TestAccessGrant_SilentWhenPrivate(t *testing.T) {
	_, errb := grantWith(t, "")
	if errb != "" {
		t.Errorf("expected no warning for a private-app grant, got %q", errb)
	}
}

// When the server warns that the app is already open to everyone (shared or
// public visibility), the CLI relays that warning, matching group-grant.
func TestAccessGrant_RelaysServerWarning(t *testing.T) {
	_, errb := grantWith(t, "app is shared; all signed-in users can already view it")
	if !strings.Contains(errb, "warning: app is shared") {
		t.Errorf("expected the server warning on stderr, got %q", errb)
	}
}

// The help text must not repeat the retired false claim that member grants
// require shared visibility. Grants admit users to private apps; shared means
// every signed-in user can view regardless of membership.
func TestAccessGrantHelp_TeachesCorrectModel(t *testing.T) {
	long := newAppsAccessGrantCmd().Long
	for _, stale := range []string{"only take effect", "still cannot reach"} {
		if strings.Contains(long, stale) {
			t.Errorf("grant help still contains the false claim %q:\n%s", stale, long)
		}
	}
	for _, want := range []string{"private", "signed-in"} {
		if !strings.Contains(long, want) {
			t.Errorf("grant help should mention %q, got:\n%s", want, long)
		}
	}
}

// `apps access set` is where visibility is chosen, so its help must define what
// each level actually admits.
func TestAccessSetHelp_DefinesLevels(t *testing.T) {
	long := newAppsAccessSetCmd().Long
	for _, want := range []string{"private", "every signed-in user", "public", "internal"} {
		if !strings.Contains(long, want) {
			t.Errorf("access set help should mention %q, got:\n%s", want, long)
		}
	}
}

// The deploy --visibility flag is the other place visibility is chosen; its
// one-line usage must carry the same level definitions.
func TestDeployVisibilityFlagHelp_DefinesLevels(t *testing.T) {
	flag := newDeployCmd().Flags().Lookup("visibility")
	if flag == nil {
		t.Fatal("deploy has no --visibility flag")
	}
	for _, want := range []string{"members only", "every signed-in user", "anyone", "internal"} {
		if !strings.Contains(flag.Usage, want) {
			t.Errorf("--visibility usage should mention %q, got: %s", want, flag.Usage)
		}
	}
}

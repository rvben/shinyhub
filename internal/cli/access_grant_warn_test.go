package cli

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func grantWith(t *testing.T, appAccess string) (out, errb string) {
	t.Helper()
	setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if appAccess != "" {
			w.Header().Set("X-Shinyhub-App-Access", appAccess)
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

func TestAccessGrant_WarnsWhenPrivate(t *testing.T) {
	_, errb := grantWith(t, "private")
	if !strings.Contains(strings.ToLower(errb), "private") {
		t.Errorf("expected a private-app warning on stderr, got %q", errb)
	}
	if !strings.Contains(errb, "shared") {
		t.Errorf("warning should point at making the app shared, got %q", errb)
	}
}

func TestAccessGrant_NoWarnWhenShared(t *testing.T) {
	_, errb := grantWith(t, "shared")
	if strings.Contains(strings.ToLower(errb), "private") {
		t.Errorf("did not expect a private warning for a shared app, got %q", errb)
	}
}

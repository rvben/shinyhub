package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestWhoami_ShowsIdentity(t *testing.T) {
	srv, reqs, setResp := setupCLITest(t)
	setResp(200, `{"user":{"id":3,"username":"dakota","role":"developer"},"can_create_apps":true}`)

	cmd := newWhoamiCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := out.String()
	for _, want := range []string{"dakota", "developer", srv.URL} {
		if !strings.Contains(got, want) {
			t.Errorf("whoami output %q missing %q", got, want)
		}
	}
	// It must consult /api/auth/me, not guess from the local config.
	if len(*reqs) != 1 || (*reqs)[0].Path != "/api/auth/me" {
		t.Errorf("expected one GET /api/auth/me, got %+v", *reqs)
	}
}

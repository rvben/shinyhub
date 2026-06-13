package cli

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestEnvSet_NudgesRestartWhenRequired(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"key":"FOO","changed":true,"restart_required":true}`)

	cmd := newEnvCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs([]string{"set", "demo", "FOO=bar"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(strings.ToLower(errb.String()), "restart") {
		t.Errorf("expected restart nudge on stderr, got %q", errb.String())
	}
	if !strings.Contains(out.String(), "restart_required") {
		t.Errorf("expected restart_required in JSON output, got %q", out.String())
	}
}

func TestEnvSet_NoNudgeWhenNotRequired(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"key":"FOO","changed":true,"restart_required":false}`)

	cmd := newEnvCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs([]string{"set", "demo", "FOO=bar"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(strings.ToLower(errb.String()), "restart") {
		t.Errorf("did not expect a restart nudge, got %q", errb.String())
	}
}

func TestEnvRm_NudgesRestartFromHeader(t *testing.T) {
	setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Shinyhub-Restart-Required", "true")
		w.WriteHeader(http.StatusNoContent)
	})

	cmd := newEnvCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs([]string{"rm", "demo", "FOO"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(strings.ToLower(errb.String()), "restart") {
		t.Errorf("expected restart nudge on stderr, got %q", errb.String())
	}
}

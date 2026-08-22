package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestWhoami_ShowsIdentity(t *testing.T) {
	srv, reqs, setResp := setupCLITest(t)
	setResp(200, `{"user":{"id":3,"username":"dakota","role":"developer"},"can_create_apps":true,"credential":{"type":"api_key","id":8,"name":"cli-laptop","created_at":"2026-05-01T00:00:00Z","last_used_at":"2026-08-01T00:00:00Z","expires_at":"2099-10-01T00:00:00Z"}}`)

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
	var result struct {
		Credential credentialLifecycle `json:"credential"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode whoami output: %v", err)
	}
	if result.Credential.Type != "api_key" || result.Credential.Name != "cli-laptop" || result.Credential.Status != "healthy" || result.Credential.LastUsedAt == nil {
		t.Fatalf("credential lifecycle = %+v", result.Credential)
	}
	// It must consult /api/auth/me, not guess from the local config.
	if len(*reqs) != 1 || (*reqs)[0].Path != "/api/auth/me" {
		t.Errorf("expected one GET /api/auth/me, got %+v", *reqs)
	}
}

func TestWhoami_ReportsCredentialAppScope(t *testing.T) {
	srv, _, setResp := setupCLITest(t)
	setResp(200, `{"user":{"id":3,"username":"__deploy__","role":"developer"},"can_create_apps":true,"app_scope":["energy","observability"],"credential":{"type":"deploy_token"}}`)

	cmd := newWhoamiCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result struct {
		CanCreateApps bool     `json:"can_create_apps"`
		AppScope      []string `json:"app_scope"`
		Host          string   `json:"host"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode whoami output: %v", err)
	}
	if !result.CanCreateApps || result.Host != srv.URL {
		t.Fatalf("identity result = %+v", result)
	}
	if got := strings.Join(result.AppScope, ","); got != "energy,observability" {
		t.Fatalf("app_scope = %q, want energy,observability", got)
	}
}

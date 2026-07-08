package config_test

import (
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

func TestLoad_DeployTokenAppsFromEnv(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 32))
	t.Setenv("SHINYHUB_DEPLOY_TOKEN", "shk_"+strings.Repeat("b", 64))
	t.Setenv("SHINYHUB_DEPLOY_TOKEN_APPS", "sales, hr-dashboard")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"sales", "hr-dashboard"}
	if len(cfg.Auth.DeployTokenApps) != len(want) {
		t.Fatalf("DeployTokenApps = %v, want %v", cfg.Auth.DeployTokenApps, want)
	}
	for i := range want {
		if cfg.Auth.DeployTokenApps[i] != want[i] {
			t.Errorf("DeployTokenApps[%d] = %q, want %q", i, cfg.Auth.DeployTokenApps[i], want[i])
		}
	}
}

func TestLoad_DeployTokenAppsWithoutToken(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 32))
	t.Setenv("SHINYHUB_DEPLOY_TOKEN_APPS", "sales")

	_, err := config.Load("")
	if err == nil {
		t.Fatal("expected error when deploy_token_apps is set without a deploy token")
	}
	if !strings.Contains(err.Error(), "deploy_token_apps") {
		t.Errorf("error %q should mention deploy_token_apps", err)
	}
}

func TestLoad_DeployTokenAppsInvalidSlug(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 32))
	t.Setenv("SHINYHUB_DEPLOY_TOKEN", "shk_"+strings.Repeat("b", 64))
	t.Setenv("SHINYHUB_DEPLOY_TOKEN_APPS", "Bad_Slug!")

	_, err := config.Load("")
	if err == nil {
		t.Fatal("expected error for an invalid slug in deploy_token_apps")
	}
	if !strings.Contains(err.Error(), "deploy_token_apps") {
		t.Errorf("error %q should mention deploy_token_apps", err)
	}
}

func TestLoad_OperatorAuditAccessFromEnv(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 32))
	t.Setenv("SHINYHUB_OPERATOR_AUDIT_ACCESS", "true")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Auth.OperatorAuditAccess {
		t.Error("OperatorAuditAccess should be true from env")
	}
}

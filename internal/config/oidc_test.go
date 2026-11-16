package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOIDCPolicyValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  OIDCConfig
		fail bool
	}{
		{name: "defaults"}, {name: "unknown missing policy", cfg: OIDCConfig{MissingGroups: "ignore"}, fail: true},
		{name: "email linking refused", cfg: OIDCConfig{LinkExisting: "email"}, fail: true},
		{name: "claim required for linking", cfg: OIDCConfig{LinkExisting: "username"}, fail: true},
		{name: "linking requires required username", cfg: OIDCConfig{UsernameClaim: "corp", LinkExisting: "username"}, fail: true},
		{name: "graph shared authority", cfg: OIDCConfig{GraphGroups: true, IssuerURL: "https://login.microsoftonline.com/common/v2.0"}, fail: true},
		{name: "graph tenant", cfg: OIDCConfig{GraphGroups: true, IssuerURL: "https://login.microsoftonline.com/tenant/v2.0"}},
		{name: "logout insecure callback", cfg: OIDCConfig{EndSession: true, CallbackURL: "http://app.example/api/auth/oidc/callback"}, fail: true},
		{name: "logout secure callback", cfg: OIDCConfig{EndSession: true, CallbackURL: "https://app.example/api/auth/oidc/callback"}},
		{name: "logout local development", cfg: OIDCConfig{EndSession: true, CallbackURL: "http://localhost:8080/api/auth/oidc/callback"}},
		{name: "strict username", cfg: OIDCConfig{UsernameClaim: "corp", UsernameRequired: true}},
		{name: "graph wrong issuer", cfg: OIDCConfig{GraphGroups: true, IssuerURL: "https://example.com"}, fail: true},
		{name: "graph roles incompatible", cfg: OIDCConfig{GraphGroups: true, GroupsClaim: "roles", IssuerURL: "https://login.microsoftonline.com/tenant/v2.0"}, fail: true},
		{name: "logout malformed callback", cfg: OIDCConfig{EndSession: true, CallbackURL: "https://app/callback"}, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); (err != nil) != tc.fail {
				t.Fatalf("validation=%v", err)
			}
		})
	}
}

func TestOIDCPolicyYAMLAndEnvironment(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "test-secret-with-more-than-thirty-two-characters")
	path := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(path, []byte(`oauth:
  oidc:
    username_claim: corp
    username_required: true
    username_strip_domain: true
    link_existing: username
    missing_groups: empty
    require_groups: true
    auto_redirect: true
`), 0600)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	o := cfg.OAuth.OIDC
	if o.UsernameClaim != "corp" || !o.UsernameRequired || !o.UsernameStripDomain || o.LinkExisting != "username" || o.MissingGroups != "empty" || !o.RequireGroups || !o.AutoRedirect {
		t.Fatalf("YAML not applied: %+v", o)
	}
	t.Setenv("SHINYHUB_OIDC_USERNAME_CLAIM", "other")
	t.Setenv("SHINYHUB_OIDC_USERNAME_REQUIRED", "false")
	t.Setenv("SHINYHUB_OIDC_USERNAME_STRIP_DOMAIN", "false")
	t.Setenv("SHINYHUB_OIDC_LINK_EXISTING", "off")
	t.Setenv("SHINYHUB_OIDC_MISSING_GROUPS", "reject")
	t.Setenv("SHINYHUB_OIDC_REQUIRE_GROUPS", "false")
	t.Setenv("SHINYHUB_OIDC_AUTO_REDIRECT", "false")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	o = cfg.OAuth.OIDC
	if o.UsernameClaim != "other" || o.UsernameRequired || o.UsernameStripDomain || o.LinkExisting != "off" || o.MissingGroups != "reject" || o.RequireGroups || o.AutoRedirect {
		t.Fatalf("env not applied: %+v", o)
	}
}

func TestOIDCAutomaticLinkRejectsMutableClaims(t *testing.T) {
	for _, claim := range []string{"email", "preferred_username", "upn", "unique_name", "name", "sub"} {
		if err := (OIDCConfig{UsernameClaim: claim, UsernameRequired: true, LinkExisting: "username"}).Validate(); err == nil {
			t.Fatalf("mutable linking claim accepted: %s", claim)
		}
	}
}
func TestOIDCValidationRunsAfterEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("oauth:\n  oidc:\n    username_claim: corp\n    username_required: true\n    link_existing: username\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHINYHUB_AUTH_SECRET", "test-secret-with-more-than-thirty-two-characters")
	t.Setenv("SHINYHUB_OIDC_USERNAME_REQUIRED", "false")
	if _, err := Load(path); err == nil {
		t.Fatal("invalid environment override accepted")
	}
}

func TestOIDCDocumentedCorporateBaselineLoads(t *testing.T) {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "SHINYHUB_") {
			t.Setenv(name, "")
		}
	}
	t.Setenv("SHINYHUB_AUTH_SECRET", "documentation-test-secret-at-least-32-characters")
	doc, err := os.ReadFile("../../docs/native-oidc.md")
	if err != nil {
		t.Fatal(err)
	}
	_, baseline, ok := strings.Cut(string(doc), "A corporate baseline")
	if !ok {
		t.Fatal("baseline missing")
	}
	_, baseline, ok = strings.Cut(baseline, "```yaml\n")
	if !ok {
		t.Fatal("YAML missing")
	}
	baseline, _, ok = strings.Cut(baseline, "```")
	if !ok {
		t.Fatal("YAML incomplete")
	}
	path := filepath.Join(t.TempDir(), "baseline.yaml")
	if err := os.WriteFile(path, []byte(baseline), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Auth.GroupRoleMappings) != 2 || cfg.Auth.GroupRoleMappings[0].Role != "admin" {
		t.Fatal("baseline role mappings lost")
	}
}

package cli

import (
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/spf13/cobra"
)

func TestConfigPath_UsesShinyhubDir(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	got := configPath()
	want := filepath.Join("/home/user", ".config", "shinyhub", "config.json")
	if got != want {
		t.Fatalf("configPath() = %q, want %q", got, want)
	}
}

func TestLoadConfig_ErrorMentionsShinyhub(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error when config missing")
	}
	if !strings.Contains(err.Error(), "shinyhub login") {
		t.Fatalf("error %q should mention `shinyhub login`", err.Error())
	}
}

func TestAddCommandsTo_RegistersAllSubcommands(t *testing.T) {
	parent := &cobra.Command{Use: "parent"}
	AddCommandsTo(parent)

	wantSubcommands := []string{"login", "logout", "deploy", "apps", "tokens", "env", "data", "schedule", "share", "fleet", "manifest", "schema"}
	have := make(map[string]bool)
	for _, sub := range parent.Commands() {
		have[sub.Name()] = true
	}
	for _, name := range wantSubcommands {
		if !have[name] {
			t.Errorf("AddCommandsTo did not register %q; registered = %v", name, parent.Commands())
		}
	}
}

func TestSetVersion_PropagatesToRootCmd(t *testing.T) {
	// SetVersion should update the internal version for commands that read it.
	SetVersion("v9.9.9-test")
	if version != "v9.9.9-test" {
		t.Fatalf("SetVersion did not update version, got %q", version)
	}
}

func TestConfigPath_HonorsOverride(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Setenv("SHINYHUB_CONFIG", "")
	configPathOverride = "/tmp/custom.json"
	t.Cleanup(func() { configPathOverride = "" })

	if got, want := configPath(), "/tmp/custom.json"; got != want {
		t.Fatalf("configPath() = %q, want %q (override)", got, want)
	}
}

func TestConfigPath_HonorsEnvVar(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Setenv("SHINYHUB_CONFIG", "/etc/ci/shinyhub.json")
	configPathOverride = ""

	if got, want := configPath(), "/etc/ci/shinyhub.json"; got != want {
		t.Fatalf("configPath() = %q, want %q (env)", got, want)
	}
}

func TestConfigPath_FlagOverridesEnv(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Setenv("SHINYHUB_CONFIG", "/etc/ci/shinyhub.json")
	configPathOverride = "/explicit/flag.json"
	t.Cleanup(func() { configPathOverride = "" })

	if got, want := configPath(), "/explicit/flag.json"; got != want {
		t.Fatalf("flag must beat env: configPath() = %q, want %q", got, want)
	}
}

// LoadConfig must accept SHINYHUB_HOST + SHINYHUB_TOKEN with no on-disk
// config file at all — this is the CI / one-off scripting path.
func TestLoadConfig_FromEnvOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHINYHUB_HOST", "https://ci.example.com")
	t.Setenv("SHINYHUB_TOKEN", "shk_ci")
	t.Setenv("SHINYHUB_CONFIG", "")
	configPathOverride = ""

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig from env: %v", err)
	}
	if cfg.Host != "https://ci.example.com" || cfg.Token != "shk_ci" {
		t.Errorf("got %+v, want host/token from env", cfg)
	}
}

// SHINYHUB_HOST should override the host saved in the config while still
// reusing the saved token. This is the "target a different server" case.
func TestLoadConfig_EnvOverridesFileHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHINYHUB_HOST", "https://other.example.com")
	t.Setenv("SHINYHUB_TOKEN", "")
	t.Setenv("SHINYHUB_CONFIG", "")
	configPathOverride = ""

	if err := saveConfig(&cliConfig{Host: "https://saved.example.com", Token: "shk_saved"}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Host != "https://other.example.com" {
		t.Errorf("env should override host, got %q", cfg.Host)
	}
	if cfg.Token != "shk_saved" {
		t.Errorf("token should fall through from file, got %q", cfg.Token)
	}
}

// authHeader must pick the scheme that matches the credential type.
//
// shk_-prefixed API keys and opaque pre-shared deploy tokens (the
// SHINYHUB_DEPLOY_TOKEN contract: any >=32-char secret — hex, UUID, base64 —
// with no required prefix) are validated server-side only under the Token
// scheme. A JWT minted by POST /api/auth/login (the username/password login
// flow) is validated only under Bearer. Sending an opaque deploy token as
// Bearer routes it into JWT validation and 401s — the bug this asserts is
// fixed end-to-end (the CLI half that issue #13 left undelivered).
func TestAuthHeader_SchemeMatchesCredentialType(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  string
	}{
		{"shk_ api key", "shk_abcdef1234567890", "Token shk_abcdef1234567890"},
		{
			"opaque deploy token (openssl rand -hex 32)",
			"3f8a1c9b7e2d4f6a8b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a",
			"Token 3f8a1c9b7e2d4f6a8b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a",
		},
		{
			"opaque deploy token (UUID)",
			"f47ac10b-58cc-4372-a567-0e02b2c3d479",
			"Token f47ac10b-58cc-4372-a567-0e02b2c3d479",
		},
		{
			"opaque deploy token (base64, contains dots only in payload-free secret)",
			"ZHVtbXktc2VjcmV0LXZhbHVlLXdpdGgtMzItcGx1cy1jaGFycw==",
			"Token ZHVtbXktc2VjcmV0LXZhbHVlLXdpdGgtMzItcGx1cy1jaGFycw==",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := authHeader(tc.token); got != tc.want {
				t.Fatalf("authHeader(%q) = %q, want %q", tc.token, got, tc.want)
			}
		})
	}
}

// A real JWT produced by the exact server code path (auth.IssueJWT, the same
// call POST /api/auth/login makes) must be sent under the Bearer scheme so the
// username/password login flow keeps working. This pins the JWT branch to the
// production token shape rather than a hand-written look-alike.
func TestAuthHeader_RealLoginJWTUsesBearer(t *testing.T) {
	tok, err := auth.IssueJWT(42, "alice", "developer", "test-secret")
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	got := authHeader(tok)
	if !strings.HasPrefix(got, "Bearer ") {
		t.Fatalf("authHeader(real login JWT) = %q, want Bearer scheme", got)
	}
	if got != "Bearer "+tok {
		t.Fatalf("authHeader mangled the token: got %q, want %q", got, "Bearer "+tok)
	}
}

// End-to-end across the scheme boundary: a header built by the CLI's real
// authHeader for an opaque (non-shk_) SHINYHUB_DEPLOY_TOKEN must be accepted
// by the server's real auth entry point (auth.AuthenticateRequest, the exact
// function BearerMiddleware calls) when keyLookup is backed by a real
// auth.DeployToken. This is the assertion issue #13 should have carried but
// did not: it verified only the server's format relaxation, never that the
// CLI presents an opaque token under a scheme the server's keyLookup path
// can see. Without the structural-JWT fix the CLI sends Bearer, the server
// runs JWT validation instead of keyLookup, and this fails with 401.
func TestAuthHeader_OpaqueDeployTokenAcceptedByServerAuth(t *testing.T) {
	opaqueTokens := []string{
		"3f8a1c9b7e2d4f6a8b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a", // openssl rand -hex 32
		"f47ac10b-58cc-4372-a567-0e02b2c3d479",                             // uuidgen
		"shk_legacy_prefixed_deploy_token_value_kept_for_compat",           // pre-#13 / downstream workaround
	}
	for _, raw := range opaqueTokens {
		t.Run(raw, func(t *testing.T) {
			deployUser := &auth.ContextUser{ID: -1, Username: "__deploy__", Role: "developer"}
			dt := auth.NewDeployToken(raw, deployUser)
			keyLookup := func(hash string) (*auth.ContextUser, error) {
				if dt.Matches(hash) {
					return dt.User(), nil
				}
				return nil, fmt.Errorf("api key not found")
			}

			req := httptest.NewRequest("GET", "/api/auth/me", nil)
			req.Header.Set("Authorization", authHeader(raw))

			user, _, err := auth.AuthenticateRequest(req, "server-secret", keyLookup, nil, nil)
			if err != nil {
				t.Fatalf("server rejected CLI-built header for opaque deploy token: %v", err)
			}
			if user == nil || user.Username != "__deploy__" {
				t.Fatalf("got user %+v, want synthetic __deploy__ user", user)
			}
		})
	}
}

// AddCommandsTo must register the --config persistent flag so every subcommand
// inherits it. Without that, only `login` could be retargeted at a different
// credentials file.
func TestAddCommandsTo_RegistersConfigFlag(t *testing.T) {
	parent := &cobra.Command{Use: "parent"}
	AddCommandsTo(parent)
	f := parent.PersistentFlags().Lookup("config")
	if f == nil {
		t.Fatalf("AddCommandsTo did not register --config persistent flag")
	}
}

// TestAddCommandsTo_RegistersLogout makes sure the logout command is wired
// onto the root.
func TestAddCommandsTo_RegistersLogout(t *testing.T) {
	parent := &cobra.Command{Use: "parent"}
	AddCommandsTo(parent)
	for _, sub := range parent.Commands() {
		if sub.Name() == "logout" {
			return
		}
	}
	t.Fatalf("AddCommandsTo did not register `logout` subcommand")
}

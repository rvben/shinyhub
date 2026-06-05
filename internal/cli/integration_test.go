package cli

import (
	"bytes"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// bootDeployTokenServer stands up the genuine production server stack — real
// SQLite store, real api.New router (chi + BearerMiddleware), the deploy token
// registered exactly the way cmd/shinyhub/main.go does — fronted by an
// httptest server. It returns the base URL and the raw opaque token the CLI
// should present.
//
// This is deliberately the full HTTP path, not auth.AuthenticateRequest in
// isolation: the bug that shipped (issue #13's CLI half) survived precisely
// because every prior CLI test pointed at a fake server that accepted any
// Authorization value, so the scheme was never validated end-to-end.
func bootDeployTokenServer(t *testing.T, rawToken string) string {
	t.Helper()

	store := dbtest.New(t)
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret", DeployTokenRole: "developer"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
	}
	srv := api.New(cfg, store, nil, nil)

	// Mirror cmd/shinyhub/main.go: provision the synthetic __deploy__ user and
	// bind the opaque token to it before the server handles any request.
	sysUser, err := store.UpsertSystemUser(db.SystemUsernameDeploy, cfg.Auth.DeployTokenRole)
	if err != nil {
		t.Fatalf("upsert system user: %v", err)
	}
	srv.SetDeployToken(auth.NewDeployToken(rawToken, &auth.ContextUser{
		ID:       sysUser.ID,
		Username: sysUser.Username,
		Role:     sysUser.Role,
	}))

	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return ts.URL
}

// runApps executes the real `apps` cobra subcommand end to end against the
// test server, through the exact wiring the shipped binary uses. It points
// the CLI at the test server via env (SHINYHUB_HOST/SHINYHUB_TOKEN drive
// loadConfig; SHINYHUB_CONFIG points at a nonexistent path so the env-only
// credential path is exercised), then delegates dispatch to execCLI. args are
// the `apps` sub-arguments (e.g. "list"); the "apps" prefix is added here.
func runApps(t *testing.T, host, token string, args ...string) (string, error) {
	t.Helper()

	t.Setenv("SHINYHUB_HOST", host)
	t.Setenv("SHINYHUB_TOKEN", token)
	t.Setenv("SHINYHUB_CONFIG", filepath.Join(t.TempDir(), "nonexistent.json"))
	configPathOverride = ""
	t.Cleanup(func() {
		configPathOverride = ""
	})

	return execCLI(t, append([]string{"apps"}, args...)...)
}

// An opaque (non-shk_) SHINYHUB_DEPLOY_TOKEN must authenticate real CLI
// subcommands against the real server, end to end. This is the assertion
// issue #13 should have carried: the server accepting the opaque token is
// worthless to operators if the CLI cannot present it under a scheme the
// server's keyLookup path sees. Subtests cover every documented opaque shape
// plus the legacy shk_-prefixed form (the downstream workaround) — all must
// work identically through the full HTTP stack.
func TestIntegration_OpaqueDeployTokenAuthenticatesRealCLI(t *testing.T) {
	tokens := map[string]string{
		"openssl rand -hex 32": "3f8a1c9b7e2d4f6a8b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a",
		"uuidgen":              "f47ac10b-58cc-4372-a567-0e02b2c3d479",
		"base64 secret":        "ZHVtbXktc2VjcmV0LXZhbHVlLXdpdGgtMzItcGx1cy1jaGFycw==",
		"legacy shk_ prefix":   "shk_legacy_prefixed_deploy_token_value_for_compat",
	}
	for name, tok := range tokens {
		t.Run(name, func(t *testing.T) {
			host := bootDeployTokenServer(t, tok)

			// `shinyhub apps list` — the exact command #13's repro showed
			// returning 401. A fresh store has no apps, so success is the
			// "No apps." line with no error.
			out, err := runApps(t, host, tok, "list")
			if err != nil {
				t.Fatalf("apps list failed under opaque deploy token: %v\noutput: %s", err, out)
			}
			if !strings.Contains(out, "No apps.") {
				t.Fatalf("apps list did not authenticate cleanly; output: %q", out)
			}

			// App creation (POST /api/apps) — the other failure #13's repro
			// hit ("could not create app t: unauthorized"). ensureApp is the
			// production deploy precondition; it builds its header via the
			// same authHeader. A 201 here proves the write path too.
			var stderr bytes.Buffer
			if err := ensureAppWithOutput(&cliConfig{Host: host, Token: tok}, "intgr", "", &stderr); err != nil {
				t.Fatalf("ensureApp (create) failed under opaque deploy token: %v\nstderr: %s", err, stderr.String())
			}

			// The created app is now visible — proves the request was
			// attributed to the authenticated __deploy__ user, not silently
			// dropped.
			out, err = runApps(t, host, tok, "list")
			if err != nil {
				t.Fatalf("apps list after create: %v\noutput: %s", err, out)
			}
			if !strings.Contains(out, "intgr") {
				t.Fatalf("created app not listed; got: %q", out)
			}
		})
	}
}

// Negative control: a token that does NOT match the server's configured
// deploy token must be rejected. Without this, the positive test could pass
// vacuously if the harness failed open (the exact failure mode — a server
// that accepts anything — that hid the original bug). The CLI must surface
// the server's 401, not succeed.
func TestIntegration_WrongDeployTokenIsRejected(t *testing.T) {
	host := bootDeployTokenServer(t, "3f8a1c9b7e2d4f6a8b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a")

	wrong := "0000000000000000000000000000000000000000000000000000000000000000"
	out, err := runApps(t, host, wrong, "list")
	if err == nil {
		t.Fatalf("apps list unexpectedly succeeded with a non-matching token; output: %q", out)
	}
	if !strings.Contains(strings.ToLower(out+err.Error()), "unauthorized") {
		t.Fatalf("expected an unauthorized failure, got: %v / %q", err, out)
	}
}

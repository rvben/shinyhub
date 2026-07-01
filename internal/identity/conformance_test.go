package identity_test

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/identity"
)

// TestConformance_HelpersVerifyRealToken proves the shipped Python and R client
// helpers verify a token minted by the REAL production MintToken (same key
// derivation, claims, and signing the proxy uses). It is gated behind
// SHINYHUB_CONFORMANCE=1 (set by `make test-identity-conformance`) so the
// default `go test ./...` does not require uv/Rscript. Each language subtest
// skips cleanly when its toolchain is absent.
func TestConformance_HelpersVerifyRealToken(t *testing.T) {
	if os.Getenv("SHINYHUB_CONFORMANCE") != "1" {
		t.Skip("set SHINYHUB_CONFORMANCE=1 (make test-identity-conformance) to run cross-language helper conformance")
	}

	key := identity.DeriveKey("conformance-secret", 42)
	keyHex := hex.EncodeToString(key)
	const slug = "sales-dashboard"
	tok, err := identity.MintToken(key, identity.TokenParams{
		UserID: 42, Username: "alice", Role: "admin", Email: "alice@example.com",
		Groups: []string{"team-a", "team-b"}, Slug: slug,
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	assertAlice := func(t *testing.T, got map[string]any) {
		t.Helper()
		if got == nil {
			t.Fatal("helper returned anonymous for a valid production token")
		}
		if got["username"] != "alice" || got["role"] != "admin" || got["user_id"] != "42" ||
			got["email"] != "alice@example.com" {
			t.Errorf("helper decoded %v, want username=alice role=admin user_id=42 email=alice@example.com", got)
		}
	}

	t.Run("python", func(t *testing.T) {
		if _, err := exec.LookPath("uv"); err != nil {
			t.Skip("uv not available")
		}
		src, _ := filepath.Abs("../../packaging/python-identity/src")
		script := `import os, json
from shinyhub_identity import current_user
u = current_user({"x-shinyhub-identity-token": os.environ["TOK"]})
print(json.dumps(None if u is None else {"username": u.username, "role": u.role, "user_id": u.user_id, "email": u.email, "groups": list(u.groups)}))`
		cmd := exec.Command("uv", "run", "--with", "pyjwt", "--no-project", "python", "-c", script)
		cmd.Env = append(os.Environ(),
			"PYTHONPATH="+src, "TOK="+tok,
			"SHINYHUB_IDENTITY_KEY="+keyHex, "SHINYHUB_APP_SLUG="+slug)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("python helper failed: %v\n%s", err, out)
		}
		assertAlice(t, lastJSONLine(t, out))
	})

	t.Run("r", func(t *testing.T) {
		if _, err := exec.LookPath("Rscript"); err != nil {
			t.Skip("Rscript not available")
		}
		rfile, _ := filepath.Abs("../../packaging/r-identity/R/identity.R")
		script := `source(Sys.getenv("RFILE"))
u <- verify_token(Sys.getenv("TOK"), key = Sys.getenv("SHINYHUB_IDENTITY_KEY"), slug = Sys.getenv("SHINYHUB_APP_SLUG"))
if (is.null(u)) cat("null\n") else cat(sprintf('{"username":"%s","role":"%s","user_id":"%s","email":"%s"}\n', u$preferred_username, u$role, as.character(u$sub), u$email))`
		cmd := exec.Command("Rscript", "-e", script)
		cmd.Env = append(os.Environ(),
			"RFILE="+rfile, "TOK="+tok,
			"SHINYHUB_IDENTITY_KEY="+keyHex, "SHINYHUB_APP_SLUG="+slug)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("R helper failed: %v\n%s", err, out)
		}
		assertAlice(t, lastJSONLine(t, out))
	})
}

// lastJSONLine parses the final non-empty output line as a JSON object, or nil
// for a "null"/"None" anonymous result.
func lastJSONLine(t *testing.T, out []byte) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if last == "null" || last == "None" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(last), &m); err != nil {
		t.Fatalf("parse helper output %q: %v (full: %s)", last, err, out)
	}
	return m
}

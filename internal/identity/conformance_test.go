package identity_test

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rvben/shinyhub/internal/identity"
)

// The conformance tests prove the shipped Python and R client helpers behave
// identically against tokens minted by the REAL production MintToken (same key
// derivation, claims, and signing the proxy uses). They are gated behind
// SHINYHUB_CONFORMANCE=1 (set by `make test-identity-conformance`) so the
// default `go test ./...` does not require uv/Rscript. Each language subtest
// skips cleanly when its toolchain is absent.

const conformanceSlug = "sales-dashboard"

func requireConformance(t *testing.T) {
	t.Helper()
	if os.Getenv("SHINYHUB_CONFORMANCE") != "1" {
		t.Skip("set SHINYHUB_CONFORMANCE=1 (make test-identity-conformance) to run cross-language helper conformance")
	}
}

// runHelper runs a helper script in one language and returns its last output
// line parsed as JSON. env carries the SHINYHUB_* variables under test.
func runHelper(t *testing.T, lang string, env []string, pyScript, rScript string) map[string]any {
	t.Helper()
	var cmd *exec.Cmd
	switch lang {
	case "python":
		if _, err := exec.LookPath("uv"); err != nil {
			t.Skip("uv not available")
		}
		src, _ := filepath.Abs("../../packaging/python-identity/src")
		cmd = exec.Command("uv", "run", "--with", "pyjwt", "--no-project", "python", "-c", pyScript)
		env = append(env, "PYTHONPATH="+src)
	case "r":
		if _, err := exec.LookPath("Rscript"); err != nil {
			t.Skip("Rscript not available")
		}
		rfile, _ := filepath.Abs("../../packaging/r-identity/R/identity.R")
		cmd = exec.Command("Rscript", "-e", rScript)
		env = append(env, "RFILE="+rfile)
	default:
		t.Fatalf("unknown language %q", lang)
	}
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s helper failed: %v\n%s", lang, err, out)
	}
	return lastJSONLine(t, out)
}

// TestConformance_HelpersVerifyRealToken pins the identity both helpers return
// for a valid production token, field name by field name: the two SDKs document
// one shape, so a rename on either side must fail here.
func TestConformance_HelpersVerifyRealToken(t *testing.T) {
	requireConformance(t)

	key := identity.DeriveKey("conformance-secret", 42)
	keyHex := hex.EncodeToString(key)
	tok, err := identity.MintToken(key, identity.TokenParams{
		UserID: 42, Username: "alice", Role: "admin", Email: "alice@example.com",
		Name: "Alice Liddell", Groups: []string{"team-a", "team-b"}, Slug: conformanceSlug,
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	pyScript := `import os, json
from shinyhub_identity import current_user
u = current_user({"x-shinyhub-identity-token": os.environ["TOK"]})
print(json.dumps({"username": u.username, "role": u.role, "user_id": u.user_id,
                  "email": u.email, "name": u.name, "groups": list(u.groups),
                  "groups_truncated": u.groups_truncated}))`

	rScript := `source(Sys.getenv("RFILE"))
u <- verify_token(Sys.getenv("TOK"))
cat(jsonlite::toJSON(list(username = u$username, role = u$role, user_id = u$user_id,
                          email = u$email, name = u$name, groups = u$groups,
                          groups_truncated = u$groups_truncated), auto_unbox = TRUE), "\n")`

	env := []string{"TOK=" + tok, "SHINYHUB_IDENTITY_KEY=" + keyHex, "SHINYHUB_APP_SLUG=" + conformanceSlug}

	for _, lang := range []string{"python", "r"} {
		t.Run(lang, func(t *testing.T) {
			got := runHelper(t, lang, env, pyScript, rScript)
			if got == nil {
				t.Fatal("helper returned anonymous for a valid production token")
			}
			want := map[string]any{
				"username": "alice", "role": "admin", "user_id": "42",
				"email": "alice@example.com", "name": "Alice Liddell",
				"groups": []any{"team-a", "team-b"}, "groups_truncated": false,
			}
			for field, wantValue := range want {
				if !reflect.DeepEqual(got[field], wantValue) {
					t.Errorf("%s = %#v, want %#v (full: %v)", field, got[field], wantValue, got)
				}
			}
		})
	}
}

// TestConformance_HelpersRejectTheSameWay pins the failure contract across both
// SDKs: a token that is present but unverifiable must raise with the same
// machine-readable reason in each language, and only an absent token is
// anonymous. Divergence here is not cosmetic - it means one SDK accepts what
// the other rejects, or tells the operator a different story about why.
func TestConformance_HelpersRejectTheSameWay(t *testing.T) {
	requireConformance(t)

	key := identity.DeriveKey("conformance-secret", 42)
	otherKey := identity.DeriveKey("conformance-secret", 43) // a different app
	mint := func(t *testing.T, k []byte, slug string) string {
		t.Helper()
		tok, err := identity.MintToken(k, identity.TokenParams{
			UserID: 42, Username: "alice", Role: "admin", Slug: slug,
		})
		if err != nil {
			t.Fatalf("mint: %v", err)
		}
		return tok
	}
	// Tokens ShinyHub would never mint, signed the same way MintToken signs, so
	// both SDKs' rejection paths can be driven off one identical token. The
	// happy-path test above is what pins the production minter.
	mintDegraded := func(t *testing.T, method jwt.SigningMethod, claims jwt.MapClaims) string {
		t.Helper()
		tok, err := jwt.NewWithClaims(method, claims).SignedString(key)
		if err != nil {
			t.Fatalf("mint degraded: %v", err)
		}
		return tok
	}
	// A well-formed ShinyHub claim set, before one field is broken per case.
	goodClaims := func() jwt.MapClaims {
		return jwt.MapClaims{
			"iss": identity.Issuer, "aud": conformanceSlug, "sub": "42",
			"preferred_username": "alice", "role": "admin",
			"exp": time.Now().Add(identity.TokenTTL).Unix(),
		}
	}
	without := func(field string) jwt.MapClaims {
		c := goodClaims()
		delete(c, field)
		return c
	}

	// Both helpers report the outcome as {"reason": ...}, with "anonymous" for
	// a request that carried no token at all.
	pyScript := `import os, json
from shinyhub_identity import IdentityError, current_user
tok = os.environ["TOK"]
headers = {} if tok == "" else {"x-shinyhub-identity-token": tok}
try:
    u = current_user(headers)
    print(json.dumps({"reason": "anonymous" if u is None else "accepted"}))
except IdentityError as e:
    print(json.dumps({"reason": e.reason}))`

	rScript := `source(Sys.getenv("RFILE"))
tok <- Sys.getenv("TOK")
request <- if (nzchar(tok)) list(HTTP_X_SHINYHUB_IDENTITY_TOKEN = tok) else list()
reason <- tryCatch({
  u <- current_user(list(request = request))
  if (is.null(u)) "anonymous" else "accepted"
}, shinyhub_identity_error = function(e) e$reason)
cat(sprintf('{"reason":"%s"}\n', reason))`

	cases := []struct {
		name   string
		token  func(t *testing.T) string
		reason string
	}{
		{"bad_signature", func(t *testing.T) string { return mint(t, otherKey, conformanceSlug) }, "bad_signature"},
		{"wrong_audience", func(t *testing.T) string { return mint(t, key, "other-app") }, "wrong_audience"},
		{"malformed", func(t *testing.T) string { return "not-a-jwt-at-all" }, "malformed"},
		{"anonymous", func(t *testing.T) string { return "" }, "anonymous"},
		{"wrong_issuer", func(t *testing.T) string {
			c := goodClaims()
			c["iss"] = "evil"
			return mintDegraded(t, jwt.SigningMethodHS256, c)
		}, "wrong_issuer"},
		// 45 seconds past exp: inside jose's own 60-second grace but outside the
		// 30-second leeway both SDKs default to, so this fails if either stops
		// enforcing that leeway itself.
		{"expired", func(t *testing.T) string {
			c := goodClaims()
			c["exp"] = time.Now().Add(-45 * time.Second).Unix()
			return mintDegraded(t, jwt.SigningMethodHS256, c)
		}, "expired"},
		// A claim ShinyHub always mints but this token lacks means the token is
		// not a ShinyHub token, which is malformed rather than "wrong".
		{"missing_exp", func(t *testing.T) string {
			return mintDegraded(t, jwt.SigningMethodHS256, without("exp"))
		}, "malformed"},
		{"missing_iss", func(t *testing.T) string {
			return mintDegraded(t, jwt.SigningMethodHS256, without("iss"))
		}, "malformed"},
		{"missing_aud", func(t *testing.T) string {
			return mintDegraded(t, jwt.SigningMethodHS256, without("aud"))
		}, "malformed"},
		// Authentic under the app's own key, but not the algorithm ShinyHub
		// mints. Both SDKs pin HS256 rather than verifying whatever the token's
		// header asks for.
		{"wrong_algorithm", func(t *testing.T) string {
			return mintDegraded(t, jwt.SigningMethodHS512, goodClaims())
		}, "malformed"},
	}

	for _, tc := range cases {
		for _, lang := range []string{"python", "r"} {
			t.Run(tc.name+"/"+lang, func(t *testing.T) {
				env := []string{
					"TOK=" + tc.token(t),
					"SHINYHUB_IDENTITY_KEY=" + hex.EncodeToString(key),
					"SHINYHUB_APP_SLUG=" + conformanceSlug,
				}
				got := runHelper(t, lang, env, pyScript, rScript)
				if got["reason"] != tc.reason {
					t.Errorf("reason = %v, want %q", got["reason"], tc.reason)
				}
			})
		}
	}
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

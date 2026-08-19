package identity_test

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
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

// requireToolchain keeps the matrix runnable on a machine without uv or R,
// while making sure CI can never report a green conformance run that verified
// nothing: with SHINYHUB_IDENTITY_STRICT=1 an absent toolchain fails instead of
// skipping. A silent skip is the dangerous outcome here, because "all subtests
// passed" and "half the subtests never ran" look identical in a CI summary.
func requireToolchain(t *testing.T, bin string) {
	t.Helper()
	if _, err := exec.LookPath(bin); err == nil {
		return
	}
	if os.Getenv("SHINYHUB_IDENTITY_STRICT") == "1" {
		t.Fatalf("%s not found and SHINYHUB_IDENTITY_STRICT=1: the %s half of the conformance matrix would not have run", bin, bin)
	}
	t.Skipf("%s not available", bin)
}

// runHelper runs a helper script in one language and returns its last output
// line parsed as JSON. env carries the SHINYHUB_* variables under test.
func runHelper(t *testing.T, lang string, env []string, pyScript, rScript string) map[string]any {
	t.Helper()
	var cmd *exec.Cmd
	switch lang {
	case "python":
		requireToolchain(t, "uv")
		src, _ := filepath.Abs("../../packaging/python-identity/src")
		cmd = exec.Command("uv", "run", "--with", "pyjwt", "--no-project", "python", "-c", pyScript)
		env = append(env, "PYTHONPATH="+src)
	case "r":
		requireToolchain(t, "Rscript")
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

// TestConformance_TestHelperMatchesProduction pins the SDKs' TEST-token minters
// against the production one, claim by claim.
//
// Those minters exist so an app author can test their identity-gated code, and
// they are only worth shipping if what they produce is indistinguishable from
// what the proxy sends. A helper that is even slightly more generous - an empty
// email minted as "" instead of omitted, groups as [] instead of null, aud as a
// string instead of a one-element array - lets an app author write a green test
// for code that breaks on deploy, which is worse than having no helper at all.
//
// This is the only place the Go, Python and R minters exist at once, so it is
// the only place that drift can be caught.
func TestConformance_TestHelperMatchesProduction(t *testing.T) {
	requireConformance(t)

	// The published test-helper defaults. Mirrored here rather than imported,
	// so a change to either side shows up as a failure instead of moving both
	// at once.
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	const slug = "test-app"
	keyHex := hex.EncodeToString(key)

	pyScript := `import os, json
from shinyhub_identity.testing import mint_token
p = json.loads(os.environ["PARAMS"])
print(json.dumps({"token": mint_token(key=os.environ["KEY_HEX"], slug=os.environ["SLUG"], **p)}))`

	rScript := `source(Sys.getenv("RFILE"))
source(Sys.getenv("RTESTFILE"))
p <- jsonlite::fromJSON(Sys.getenv("PARAMS"), simplifyVector = FALSE)
tok <- shinyhub_test_token(
  user_id = p$user_id, username = p$username, role = p$role,
  app_role = p$app_role, email = p$email, name = p$name,
  groups = unlist(p$groups), groups_truncated = p$groups_truncated,
  key = Sys.getenv("KEY_HEX"), slug = Sys.getenv("SLUG")
)
cat(jsonlite::toJSON(list(token = tok), auto_unbox = TRUE), "\n")`

	rTestFile, err := filepath.Abs("../../packaging/r-identity/R/testing.R")
	if err != nil {
		t.Fatalf("resolve R testing helper: %v", err)
	}

	cases := []struct {
		name   string
		params identity.TokenParams
		// rawGroups is what an app author passes to the helper: unsorted, as it
		// comes from an IdP. Production sanitizes before minting, so the Go side
		// mints the SanitizeGroups output and the two must still agree.
		rawGroups []string
	}{
		{
			name:   "minimal",
			params: identity.TokenParams{UserID: 42, Username: "testuser", Role: "viewer"},
		},
		{
			name: "every_optional_claim",
			params: identity.TokenParams{
				UserID: 7, Username: "carol", Role: "admin", AppRole: "manager",
				Email: "carol@example.com", Name: "Carol Danvers",
				GroupsTruncated: true,
			},
			rawGroups: []string{"b-team", "a-team"},
		},
		{
			name:      "single_group",
			params:    identity.TokenParams{UserID: 1, Username: "solo", Role: "viewer"},
			rawGroups: []string{"only-team"},
		},
		{
			name:   "app_role_without_groups",
			params: identity.TokenParams{UserID: 2, Username: "owner", Role: "developer", AppRole: "owner"},
		},
	}

	for _, tc := range cases {
		for _, lang := range []string{"python", "r"} {
			t.Run(tc.name+"/"+lang, func(t *testing.T) {
				params := tc.params
				params.Slug = slug
				// Exactly what the proxy does before minting.
				if len(tc.rawGroups) > 0 {
					_, params.Groups, _ = identity.SanitizeGroups(tc.rawGroups)
				}
				production, err := identity.MintToken(key, params)
				if err != nil {
					t.Fatalf("mint production token: %v", err)
				}

				helperArgs, err := json.Marshal(map[string]any{
					"user_id":          tc.params.UserID,
					"username":         tc.params.Username,
					"role":             tc.params.Role,
					"app_role":         tc.params.AppRole,
					"email":            tc.params.Email,
					"name":             tc.params.Name,
					"groups":           append([]string{}, tc.rawGroups...),
					"groups_truncated": tc.params.GroupsTruncated,
				})
				if err != nil {
					t.Fatalf("encode helper args: %v", err)
				}

				env := []string{
					"PARAMS=" + string(helperArgs),
					"KEY_HEX=" + keyHex,
					"SLUG=" + slug,
					"RTESTFILE=" + rTestFile,
				}
				out := runHelper(t, lang, env, pyScript, rScript)
				token, ok := out["token"].(string)
				if !ok || token == "" {
					t.Fatalf("%s helper returned no token (got %v)", lang, out)
				}

				assertSameClaims(t, decodeClaims(t, production), decodeClaims(t, token))

				// The claim set matching is worth nothing if the signature does
				// not, so check the server's own verifier accepts the helper's
				// token under the same key and algorithm.
				parsed, err := jwt.Parse(token, func(*jwt.Token) (any, error) { return key, nil },
					jwt.WithValidMethods([]string{"HS256"}),
					jwt.WithIssuer(identity.Issuer),
					jwt.WithAudience(slug))
				if err != nil {
					t.Fatalf("%s helper token failed verification: %v", lang, err)
				}
				if !parsed.Valid {
					t.Fatalf("%s helper token parsed but is not valid", lang)
				}
			})
		}
	}
}

// assertSameClaims compares two decoded claim sets exactly, except for the two
// timestamps: production and the helper mint moments apart, so their absolute
// values legitimately differ. The lifetime BETWEEN them must still match, and
// both must be present - a helper that quietly dropped exp would otherwise mint
// a token with no replay bound and pass this test.
func assertSameClaims(t *testing.T, want, got map[string]any) {
	t.Helper()

	timestamps := map[string]bool{"iat": true, "exp": true}

	for _, name := range sortedKeys(want) {
		if _, present := got[name]; !present {
			t.Errorf("helper omits claim %q that production mints (value %#v)", name, want[name])
			continue
		}
		if timestamps[name] {
			continue
		}
		if !reflect.DeepEqual(got[name], want[name]) {
			t.Errorf("claim %q = %#v, production mints %#v", name, got[name], want[name])
		}
	}
	for _, name := range sortedKeys(got) {
		if _, present := want[name]; !present {
			t.Errorf("helper mints claim %q = %#v that production does not", name, got[name])
		}
	}

	for name := range timestamps {
		if _, ok := got[name].(float64); !ok {
			t.Errorf("claim %q is %#v, want a number", name, got[name])
			return
		}
	}
	if lifetime := got["exp"].(float64) - got["iat"].(float64); lifetime != identity.TokenTTL.Seconds() {
		t.Errorf("helper token lifetime is %vs, production mints %vs", lifetime, identity.TokenTTL.Seconds())
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// decodeClaims reads a token's payload WITHOUT verifying it: this compares what
// was minted, so it must not depend on the thing being checked.
func decodeClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments, want 3: %q", len(parts), token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode token payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("parse token payload %s: %v", payload, err)
	}
	return claims
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

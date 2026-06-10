package identity

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestDeriveKey_StablePerAppID(t *testing.T) {
	k1 := DeriveKey("secret-a", 42)
	k2 := DeriveKey("secret-a", 42)
	if string(k1) != string(k2) {
		t.Fatal("same (secret, appID) must derive the same key")
	}
	if len(k1) != 32 {
		t.Fatalf("key length = %d, want 32", len(k1))
	}
}

func TestDeriveKey_DiffersAcrossAppIDs(t *testing.T) {
	// Delete-and-recreate under the same slug yields a new appID and MUST
	// yield a new key.
	if string(DeriveKey("s", 1)) == string(DeriveKey("s", 2)) {
		t.Fatal("different appIDs must derive different keys")
	}
}

func TestDeriveKey_DiffersAcrossSecrets(t *testing.T) {
	if string(DeriveKey("s1", 1)) == string(DeriveKey("s2", 1)) {
		t.Fatal("different secrets must derive different keys")
	}
}

func TestSanitizeGroups_SortsAndJoins(t *testing.T) {
	header, claim, truncated := SanitizeGroups([]string{"zeta", "alpha"})
	if header != "alpha,zeta" {
		t.Fatalf("header = %q, want %q", header, "alpha,zeta")
	}
	if len(claim) != 2 || claim[0] != "alpha" || claim[1] != "zeta" {
		t.Fatalf("claim = %v", claim)
	}
	if truncated {
		t.Fatal("2 groups must not truncate")
	}
}

func TestSanitizeGroups_OmitsCommaBearingNamesFromHeaderOnly(t *testing.T) {
	// A self-service IdP group named "team,admins" must never be able to
	// forge membership for apps that split the plain header.
	header, claim, _ := SanitizeGroups([]string{"team,admins", "viewers"})
	if header != "viewers" {
		t.Fatalf("header = %q, want %q (comma-bearing name omitted)", header, "viewers")
	}
	found := false
	for _, g := range claim {
		if g == "team,admins" {
			found = true
		}
	}
	if !found {
		t.Fatal("JWT claim must still carry the comma-bearing name")
	}
}

func TestSanitizeGroups_CapsAt100Deterministically(t *testing.T) {
	in := make([]string, 150)
	for i := range in {
		in[i] = "g" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + "-" + strings.Repeat("y", i/26)
	}
	_, claim, truncated := SanitizeGroups(in)
	if len(claim) != MaxGroups {
		t.Fatalf("claim length = %d, want %d", len(claim), MaxGroups)
	}
	if !truncated {
		t.Fatal("150 groups must set truncated")
	}
	// Deterministic: sorted means a re-run yields the same first 100.
	_, claim2, _ := SanitizeGroups(in)
	for i := range claim {
		if claim[i] != claim2[i] {
			t.Fatal("cap must be deterministic (sorted before cut)")
		}
	}
}

func TestMintToken_RoundTripsWithDerivedKey(t *testing.T) {
	key := DeriveKey("secret", 7)
	tok, err := MintToken(key, TokenParams{
		UserID: 12, Username: "ruben", Role: "admin",
		Groups: []string{"a", "b"}, GroupsTruncated: false, Slug: "demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	claims := &TokenClaims{}
	parsed, err := jwt.ParseWithClaims(tok, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected alg: %v", t.Header["alg"])
		}
		return key, nil
	}, jwt.WithAudience("demo"), jwt.WithIssuer(Issuer), jwt.WithLeeway(30*time.Second))
	if err != nil || !parsed.Valid {
		t.Fatalf("verify: %v", err)
	}
	if claims.Subject != "12" || claims.PreferredUsername != "ruben" || claims.Role != "admin" {
		t.Fatalf("claims = %+v", claims)
	}
	if len(claims.Groups) != 2 {
		t.Fatalf("groups = %v", claims.Groups)
	}
}

func TestMintToken_AppAKeyRejectsAppBToken(t *testing.T) {
	keyA, keyB := DeriveKey("secret", 1), DeriveKey("secret", 2)
	tok, err := MintToken(keyB, TokenParams{UserID: 1, Username: "u", Role: "viewer", Slug: "b"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = jwt.ParseWithClaims(tok, &TokenClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected alg: %v", t.Header["alg"])
		}
		return keyA, nil
	})
	if err == nil {
		t.Fatal("app A's key must reject app B's token")
	}
}

func TestMintToken_AudMismatchRejected(t *testing.T) {
	key := DeriveKey("secret", 1)
	tok, _ := MintToken(key, TokenParams{UserID: 1, Username: "u", Role: "viewer", Slug: "appa"})
	_, err := jwt.ParseWithClaims(tok, &TokenClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected alg: %v", t.Header["alg"])
		}
		return key, nil
	}, jwt.WithAudience("appb"))
	if err == nil {
		t.Fatal("aud mismatch must be rejected")
	}
}

func TestSanitizeGroups_Empty(t *testing.T) {
	h, c, trunc := SanitizeGroups(nil)
	if h != "" || len(c) != 0 || trunc {
		t.Fatalf("nil groups: header=%q claim=%v trunc=%v", h, c, trunc)
	}
}

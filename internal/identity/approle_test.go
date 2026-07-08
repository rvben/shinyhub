package identity

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rvben/shinyhub/internal/auth"
)

// fakeSource satisfies Source with scriptable groups and app membership.
type fakeSource struct {
	fakeGroups
	memCalls   atomic.Int64
	isOwner    bool
	memberRole string
	memErr     error
}

func (f *fakeSource) AppMembershipForUser(slug string, userID int64) (bool, string, error) {
	f.memCalls.Add(1)
	return f.isOwner, f.memberRole, f.memErr
}

// TestProvider_AppRole pins the app-role policy: owner beats everything;
// global admin/operator and manager-members are "manager"; everyone else who
// reached the app is "viewer".
func TestProvider_AppRole(t *testing.T) {
	cases := []struct {
		name       string
		role       string
		isOwner    bool
		memberRole string
		want       string
	}{
		{"owner", "developer", true, "", "owner"},
		{"member-manager", "developer", false, "manager", "manager"},
		{"global-admin", "admin", false, "", "manager"},
		{"global-operator", "operator", false, "", "manager"},
		{"member-viewer", "developer", false, "viewer", "viewer"},
		{"no-membership", "viewer", false, "", "viewer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeSource{isOwner: tc.isOwner, memberRole: tc.memberRole}
			p := NewProvider("secret", src)
			pl := p.PayloadFor(&auth.ContextUser{ID: 5, Username: "ana", Role: tc.role}, "demo", 42)
			if pl.AppRole != tc.want {
				t.Errorf("AppRole = %q, want %q", pl.AppRole, tc.want)
			}
		})
	}
}

// TestProvider_AppRoleLookupErrorDegrades pins the advisory contract: a
// membership lookup failure yields an empty AppRole (header omitted), never a
// failed payload.
func TestProvider_AppRoleLookupErrorDegrades(t *testing.T) {
	src := &fakeSource{memErr: errors.New("boom")}
	p := NewProvider("secret", src)
	pl := p.PayloadFor(&auth.ContextUser{ID: 5, Username: "ana", Role: "developer"}, "demo", 42)
	if pl == nil {
		t.Fatal("payload must survive a membership lookup error")
	}
	if pl.AppRole != "" {
		t.Errorf("AppRole = %q, want empty on lookup error", pl.AppRole)
	}
}

// TestProvider_AppRoleCachedPerApp pins the cache shape: repeated requests for
// the same user+app do one membership lookup; a different app looks up again.
func TestProvider_AppRoleCachedPerApp(t *testing.T) {
	src := &fakeSource{memberRole: "manager"}
	p := NewProvider("secret", src)
	u := &auth.ContextUser{ID: 9, Username: "u", Role: "developer"}
	p.PayloadFor(u, "demo", 1)
	p.PayloadFor(u, "demo", 1)
	if got := src.memCalls.Load(); got != 1 {
		t.Fatalf("membership calls = %d, want 1 (cached per user+app)", got)
	}
	p.PayloadFor(u, "other", 2)
	if got := src.memCalls.Load(); got != 2 {
		t.Fatalf("membership calls = %d, want 2 (second app is a new key)", got)
	}
}

// TestMintToken_CarriesAppRole pins the signed claim: apps that verify the
// identity token can rely on app_role without trusting the plain header.
func TestMintToken_CarriesAppRole(t *testing.T) {
	key := DeriveKey("secret", 42)
	tok, err := MintToken(key, TokenParams{
		UserID: 5, Username: "ana", Role: "developer", Slug: "demo", AppRole: "manager",
	})
	if err != nil {
		t.Fatal(err)
	}
	var claims TokenClaims
	if _, err := jwt.ParseWithClaims(tok, &claims, func(t *jwt.Token) (any, error) { return key, nil }); err != nil {
		t.Fatal(err)
	}
	if claims.AppRole != "manager" {
		t.Errorf("app_role claim = %q, want manager", claims.AppRole)
	}
}

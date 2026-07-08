package auth_test

import (
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
)

func TestAppInScope_UnrestrictedByDefault(t *testing.T) {
	var nilUser *auth.ContextUser
	if !nilUser.AppInScope("any") {
		t.Error("nil user must be unrestricted (scope applies only to identities that set one)")
	}
	u := &auth.ContextUser{ID: 1, Username: "alice", Role: "developer"}
	if !u.AppInScope("any") {
		t.Error("a user with no AppScope must be unrestricted")
	}
}

func TestAppInScope_RestrictsToAllowlist(t *testing.T) {
	u := &auth.ContextUser{Username: "__deploy__", Role: "admin", AppScope: []string{"sales", "hr"}}
	if !u.AppInScope("sales") {
		t.Error("allowlisted slug must be in scope")
	}
	if u.AppInScope("other") {
		t.Error("slug outside the allowlist must be out of scope, regardless of role")
	}
}

func TestDeployTokenRoleWarning(t *testing.T) {
	if w := auth.DeployTokenRoleWarning("admin"); w == "" || !strings.Contains(w, "admin") {
		t.Errorf("admin role must produce a boot warning, got %q", w)
	}
	for _, role := range []string{"viewer", "developer", "operator"} {
		if w := auth.DeployTokenRoleWarning(role); w != "" {
			t.Errorf("role %s should not warn, got %q", role, w)
		}
	}
}

package auth

import "testing"

func TestResolveGlobalRole_HighestRankWins(t *testing.T) {
	mappings := []GroupRoleMapping{
		{Group: "viewers", Role: "viewer"},
		{Group: "devs", Role: "developer"},
		{Group: "admins", Role: "admin"},
	}
	role, matched := ResolveGlobalRole([]string{"devs", "admins"}, mappings, "viewer")
	if !matched || role != "admin" {
		t.Fatalf("got (%q,%v), want (admin,true)", role, matched)
	}
}

func TestResolveGlobalRole_NoMatch(t *testing.T) {
	mappings := []GroupRoleMapping{{Group: "admins", Role: "admin"}}
	role, matched := ResolveGlobalRole([]string{"strangers"}, mappings, "viewer")
	if matched || role != "" {
		t.Fatalf("got (%q,%v), want (\"\",false)", role, matched)
	}
}

func TestResolveGlobalRole_EmptyInputs(t *testing.T) {
	if _, matched := ResolveGlobalRole(nil, nil, "viewer"); matched {
		t.Fatal("nil inputs must not match")
	}
	if _, matched := ResolveGlobalRole([]string{}, []GroupRoleMapping{{Group: "a", Role: "admin"}}, "viewer"); matched {
		t.Fatal("empty groups must not match")
	}
}

func TestResolveGlobalRole_IgnoresInvalidRoleInMapping(t *testing.T) {
	mappings := []GroupRoleMapping{
		{Group: "x", Role: "superuser"}, // invalid
		{Group: "y", Role: "developer"},
	}
	role, matched := ResolveGlobalRole([]string{"x", "y"}, mappings, "viewer")
	if !matched || role != "developer" {
		t.Fatalf("got (%q,%v), want (developer,true)", role, matched)
	}
}

package auth_test

import (
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
)

func TestValidateDeployTokenFormat(t *testing.T) {
	accepted := map[string]string{
		"shk-prefixed hex":  "shk_" + strings.Repeat("a", 64),
		"plain hex":         strings.Repeat("a", 64),
		"opaque secret":     strings.Repeat("x", 40),
		"exact min length":  strings.Repeat("a", 32),
		"non-hex base64ish": "MyOperatorSecret/1234567890abcdef=",
	}
	for name, tok := range accepted {
		if err := auth.ValidateDeployTokenFormat(tok); err != nil {
			t.Errorf("%s: expected accept, got error: %v", name, err)
		}
	}

	rejected := map[string]string{
		"empty":     "",
		"too short": strings.Repeat("a", 31),
		"shk-prefixed but too short": "shk_" + strings.Repeat("a", 16),
	}
	for name, tok := range rejected {
		if err := auth.ValidateDeployTokenFormat(tok); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestDeployToken_Matches(t *testing.T) {
	raw := "shk_" + strings.Repeat("c", 64)
	dt := auth.NewDeployToken(raw, &auth.ContextUser{ID: 42, Username: "__deploy__", Role: "developer"})

	if !dt.Matches(auth.HashAPIKey(raw)) {
		t.Error("Matches should return true for the configured hash")
	}
	if dt.Matches(auth.HashAPIKey("shk_" + strings.Repeat("d", 64))) {
		t.Error("Matches should return false for a different hash")
	}
	if dt.Matches("") {
		t.Error("Matches should return false for empty hash")
	}

	u := dt.User()
	if u == nil || u.ID != 42 || u.Username != "__deploy__" || u.Role != "developer" {
		t.Errorf("User() = %+v, want {42 __deploy__ developer}", u)
	}
}

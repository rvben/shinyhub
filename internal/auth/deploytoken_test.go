package auth_test

import (
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
)

func TestValidateDeployTokenFormat(t *testing.T) {
	good := "shk_" + strings.Repeat("a", 64)
	if err := auth.ValidateDeployTokenFormat(good); err != nil {
		t.Errorf("good token rejected: %v", err)
	}

	cases := map[string]string{
		"empty":        "",
		"no prefix":    strings.Repeat("a", 68),
		"too short":    "shk_" + strings.Repeat("a", 16),
		"non-hex body": "shk_" + strings.Repeat("z", 64),
		"wrong prefix": "tok_" + strings.Repeat("a", 64),
	}
	for name, tok := range cases {
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

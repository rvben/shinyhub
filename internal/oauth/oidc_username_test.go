package oauth_test

import (
	"encoding/json"
	"testing"

	"github.com/rvben/shinyhub/internal/oauth"
)

func TestOIDCUsernamePolicy(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want       string
		required, strip, fail bool
	}{
		{name: "corporate", raw: `"abc12345"`, want: "abc12345", required: true},
		{name: "upn", raw: `"abc12345@corp.example"`, want: "abc12345", strip: true},
		{name: "missing optional"}, {name: "missing required", required: true, fail: true},
		{name: "wrong type", raw: `42`, fail: true}, {name: "empty", raw: `""`, fail: true},
		{name: "dot path", raw: `".."`, fail: true},
		{name: "unsafe", raw: `"abc/def"`, fail: true}, {name: "canonical case", raw: `"Abc_12.3"`, want: "abc_12.3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := oauth.OIDCUser{Claims: map[string]json.RawMessage{"corporate_id": json.RawMessage(tc.raw)}}
			got, err := u.Username("corporate_id", tc.required, tc.strip)
			if (err != nil) != tc.fail || got != tc.want {
				t.Fatalf("got %q %v", got, err)
			}
		})
	}
}

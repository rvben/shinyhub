package cli

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// "internal" (every signed-in user) is what most fleets actually want, but the
// canonical level is named "shared", which reads like "private plus grants".
// The CLI therefore accepts internal as an alias and always sends the
// canonical value, so the server API stays unchanged.

func TestNormalizeAccessLevel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"internal", "shared"},
		{"shared", "shared"},
		{"private", "private"},
		{"public", "public"},
		{"", ""},
		{"bogus", "bogus"}, // unknown values pass through for the server to reject
	}
	for _, tc := range cases {
		if got := normalizeAccessLevel(tc.in); got != tc.want {
			t.Errorf("normalizeAccessLevel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAccessSet_InternalAliasSendsShared(t *testing.T) {
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	cmd := newAppsCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs([]string{"access", "set", "demo", "internal"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	if got := string((*reqs)[0].Body); !strings.Contains(got, `"access":"shared"`) {
		t.Errorf("internal must normalize to shared in the request body, got: %s", got)
	}
	if !strings.Contains(out.String(), "shared") {
		t.Errorf("output should report the canonical level, got: %q", out.String())
	}
}

func TestResolveVisibilityFlag(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{"internal", "shared", false},
		{"shared", "shared", false},
		{"private", "private", false},
		{"public", "public", false},
		{"", "", false},
		{"bogus", "", true},
	}
	for _, tc := range cases {
		got, err := resolveVisibilityFlag(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("resolveVisibilityFlag(%q): expected an error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolveVisibilityFlag(%q): unexpected error %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("resolveVisibilityFlag(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

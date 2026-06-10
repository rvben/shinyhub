package oauth

import (
	"encoding/json"
	"testing"
)

func TestDecodeGroupsClaim(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   []string
		wantOK bool
	}{
		{"array", `["a","b"]`, []string{"a", "b"}, true},
		{"single", `"solo"`, []string{"solo"}, true},
		{"empty array", `[]`, []string{}, true},
		{"null", `null`, []string{}, true},
		{"empty string", `""`, []string{}, true},
		{"number malformed", `42`, nil, false},
		{"object malformed", `{"x":1}`, nil, false},
		{"number array malformed", `[1,2,3]`, nil, false},
		{"mixed array malformed", `["a",2]`, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := decodeGroupsClaim(json.RawMessage(c.in))
			if ok != c.wantOK {
				t.Errorf("ok = %v, want %v", ok, c.wantOK)
			}
			if !c.wantOK {
				// For malformed cases only check ok; slice may be nil or empty.
				return
			}
			if len(got) != len(c.want) {
				t.Errorf("got %v, want %v", got, c.want)
				return
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("got %v, want %v", got, c.want)
					return
				}
			}
		})
	}
}

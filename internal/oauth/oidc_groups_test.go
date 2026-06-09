package oauth

import (
	"encoding/json"
	"testing"
)

func TestDecodeGroupsClaim(t *testing.T) {
	cases := map[string]struct {
		in   string
		want []string
	}{
		"array":       {`["a","b"]`, []string{"a", "b"}},
		"single":      {`"solo"`, []string{"solo"}},
		"empty array": {`[]`, []string{}},
		"absent":      {`null`, []string{}},
	}
	for name, c := range cases {
		got := decodeGroupsClaim(json.RawMessage(c.in))
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v want %v", name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: got %v want %v", name, got, c.want)
			}
		}
	}
}

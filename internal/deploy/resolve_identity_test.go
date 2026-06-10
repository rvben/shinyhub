package deploy

import "testing"

func TestResolveIdentityHeaders(t *testing.T) {
	f, tr := false, true
	cases := []struct {
		col    *bool
		global bool
		want   bool
	}{
		{nil, true, true},   // inherit global on
		{nil, false, false}, // inherit global off
		{&f, true, false},   // per-app opt-out
		{&tr, true, true},   // explicit on
		{&tr, false, false}, // kill switch wins over explicit true
	}
	for _, c := range cases {
		if got := ResolveIdentityHeaders(c.col, c.global); got != c.want {
			t.Errorf("col=%v global=%v: got %v want %v", c.col, c.global, got, c.want)
		}
	}
}

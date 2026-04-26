package slug

import "testing"

func TestValid(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		// Valid: DNS-style hostname labels.
		{"a", true},
		{"a0", true},
		{"my-app", true},
		{"my-app-2024", true},
		{"abc123", true},

		// Invalid: empty, leading/trailing hyphen, uppercase, underscore, space.
		{"", false},
		{"-leading", false},
		{"trailing-", false},
		{"-", false},
		{"--", false},
		{"MyApp", false},
		{"my_app", false},
		{"my app", false},
		{"my.app", false},

		// Length bounds.
		{string(make([]byte, 0)), false},
		{repeat("a", 63), true},
		{repeat("a", 64), false},
		{"a" + repeat("b", 61) + "c", true}, // 63 chars
		{"a" + repeat("b", 62) + "c", false}, // 64 chars
	}
	for _, tc := range cases {
		if got := Valid(tc.in); got != tc.want {
			t.Errorf("Valid(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for range n {
		out = append(out, s...)
	}
	return string(out)
}

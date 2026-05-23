package cli

import "testing"

func TestUnwrapServerError(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		fallback string
		want     string
	}{
		{"standard envelope", `{"error":"app not found"}`, "request failed", "app not found"},
		{"envelope with surrounding whitespace", "  {\"error\":\"bad slug\"}\n", "request failed", "bad slug"},
		{"empty error field falls through to body", `{"error":""}`, "request failed", `{"error":""}`},
		{"non-json body trimmed", "  boom  ", "request failed", "boom"},
		{"empty body uses fallback", "", "request failed", "request failed"},
		{"whitespace-only body uses fallback", "   \n", "request failed", "request failed"},
		{"json without error field returns trimmed body", `{"message":"x"}`, "request failed", `{"message":"x"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := unwrapServerError([]byte(tc.body), tc.fallback)
			if got != tc.want {
				t.Errorf("unwrapServerError(%q, %q) = %q, want %q", tc.body, tc.fallback, got, tc.want)
			}
		})
	}
}

package favicon

import (
	"strings"
	"testing"
)

func TestBoundedIconDetectionPreservesLateAndQuotedTags(t *testing.T) {
	large := strings.Repeat("x", 1<<20)
	for _, tc := range []struct {
		page string
		icon bool
	}{
		{"<head><link rel=stylesheet href=x></head><body>" + large + "</body>", false},
		{"<head></head><body>" + large + "<LiNk rel=ICON href=late></body>", true},
		{`<head><script>var x = '<link rel=icon>';</script></head>` + large, false},
		{`<head><!-- <link rel=icon> --></head>` + large, false},
		{`<head><link title="<link >" rel=icon href=own></head>` + large, true},
		{`<head><link title="<link >" rel=stylesheet href=own></head>` + large, false},
	} {
		if got := HasIcon([]byte(tc.page)); got != tc.icon {
			t.Fatalf("icon=%t, want %t (prefix %.100s)", got, tc.icon, tc.page)
		}
	}
}

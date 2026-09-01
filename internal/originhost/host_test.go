package originhost

import "testing"

func TestHostnameBrowserEquivalentForms(t *testing.T) {
	for _, tc := range []struct{ a, b string }{
		{"bücher.example", "xn--bcher-kva.example."},
		{"[2001:0db8::1]", "[2001:db8::1]"},
	} {
		a, errA := Hostname(tc.a)
		b, errB := Hostname(tc.b)
		if errA != nil || errB != nil || a != b {
			t.Fatalf("Hostname(%q)=%q/%v Hostname(%q)=%q/%v", tc.a, a, errA, tc.b, b, errB)
		}
	}
}

func TestHostnameRejectsAmbiguousNumericIPv4(t *testing.T) {
	for _, raw := range []string{"127.1", "0177.0.0.1", "0x7f000001", "2130706433"} {
		if got, err := Hostname(raw); err == nil {
			t.Fatalf("Hostname(%q)=%q, want rejection", raw, got)
		}
	}
}

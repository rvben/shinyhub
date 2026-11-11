package availability

import "testing"

func TestExposurePrecedence(t *testing.T) {
	for _, tc := range []struct {
		intent                      string
		allow, def, central, public bool
		source                      string
		suppressed                  bool
	}{
		{"inherit", true, true, false, true, "default", false},
		{"inherit", true, false, false, false, "default", false},
		{"inherit", false, true, false, false, "none", true},
		{"off", true, true, false, false, "app", false},
		{"public", true, false, false, true, "app", false},
		{"public", false, true, false, false, "none", true},
		{"off", false, false, true, true, "allowlist", false},
		{"inherit", false, true, true, true, "allowlist", false},
		{"unknown", true, true, false, false, "none", false},
	} {
		e := Resolve(tc.intent, tc.allow, tc.def, tc.central)
		if e.Intent != tc.intent || e.Exposed != tc.public || e.Source != tc.source || e.PolicySuppressed != tc.suppressed {
			t.Errorf("%+v: got %+v", tc, e)
		}
	}
}

package deployfail

import "testing"

func TestKindValid(t *testing.T) {
	valid := []Kind{
		RuntimeMissing, BuildFailed, BundleInvalid, ReadinessTimeout,
		Crashed, ServerError, ZipError, TransportError, Unknown,
	}
	for _, k := range valid {
		if !k.Valid() {
			t.Errorf("Kind(%q).Valid() = false, want true", k)
		}
	}
	for _, bad := range []Kind{"", "nope", "Crashed", "readiness-timeout"} {
		if Kind(bad).Valid() {
			t.Errorf("Kind(%q).Valid() = true, want false", bad)
		}
	}
}

func TestKindStringValues(t *testing.T) {
	// The string values are the public schema contract; pin them.
	cases := map[Kind]string{
		RuntimeMissing:   "runtime_missing",
		BuildFailed:      "build_failed",
		BundleInvalid:    "bundle_invalid",
		ReadinessTimeout: "readiness_timeout",
		Crashed:          "crashed",
		ServerError:      "server_error",
		ZipError:         "zip_error",
		TransportError:   "transport_error",
		Unknown:          "unknown",
	}
	for k, want := range cases {
		if string(k) != want {
			t.Errorf("constant = %q, want %q", string(k), want)
		}
	}
}

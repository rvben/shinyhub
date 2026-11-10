package deploy

import "testing"

func TestManifestAvailability(t *testing.T) {
	for _, value := range []string{"off", "public"} {
		m, err := ParseManifest([]byte("[app]\navailability = \"" + value + "\"\n"))
		if err != nil || m.App.Availability == nil || *m.App.Availability != value || m.App.IsZero() {
			t.Fatalf("%s: %+v %v", value, m, err)
		}
	}
	for _, value := range []string{`"private"`, `true`, `1`, `"Public"`} {
		if _, err := ParseManifest([]byte("[app]\navailability = " + value + "\n")); err == nil {
			t.Fatalf("accepted availability=%s", value)
		}
	}
}

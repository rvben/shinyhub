package fleet

import (
	"strings"
	"testing"
)

func TestFleetAvailabilityPrecedenceAndDrift(t *testing.T) {
	src := "fleet_id='prod'\n[[app]]\nslug='demo'\nsource='./demo'\n[app.config]\navailability='public'\n"
	m, problems := ParseManifest([]byte(src), "fleet.toml")
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	off := "off"
	m.Apps[0].Bundle.Availability = &off
	c := EffectiveConfig(m.Apps[0])
	if c.Availability == nil || *c.Availability != "public" {
		t.Fatal("fleet did not override bundle")
	}
	d := configDrift(m.Apps[0], ObservedApp{Availability: &off})
	found := false
	for _, item := range d {
		if item.Key == "availability" && item.Desired == `"public"` {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing availability drift: %+v", d)
	}
	if _, problems := ParseManifest([]byte(strings.ReplaceAll(src, "'public'", "'yes'")), "fleet.toml"); len(problems) == 0 {
		t.Fatal("accepted invalid availability")
	}
}

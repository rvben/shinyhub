package fleet

import (
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/availability"
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

func TestAvailabilityPlanDoesNotReuseExposureForChangedIntent(t *testing.T) {
	m, problems := ParseManifest([]byte("fleet_id='prod'\n[[app]]\nslug='demo'\nsource='./demo'\n[app.config]\navailability='inherit'\n"), "fleet.toml")
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	off, inherit := "off", "inherit"
	exposure := &availability.Exposure{Intent: off, Source: "app"}
	observed := []ObservedApp{{Slug: "demo", Availability: &off, AvailabilityExposure: exposure}}
	if got := Diff(m, nil, observed)[0].AvailabilityExposure; got != nil {
		t.Fatalf("planned intent change reused observed exposure: %+v", got)
	}
	observed[0].Availability = &inherit
	exposure.Intent = inherit
	exposure.Source = "default"
	if got := Diff(m, nil, observed)[0].AvailabilityExposure; got != exposure {
		t.Fatalf("unchanged intent lost observed exposure: %+v", got)
	}
}

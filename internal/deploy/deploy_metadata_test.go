package deploy

import (
	"reflect"
	"testing"
)

// These assert the struct fields exist so later launch-path edits compile. They
// are deliberately field-presence checks: the runtime wiring is exercised in the
// process/manager and api integration tests.
func TestParamsCarryDeploymentMetadata(t *testing.T) {
	p := Params{ContentDigest: "sha256:abc", DeploymentID: 7, AppVersion: "v3"}
	if p.ContentDigest != "sha256:abc" || p.DeploymentID != 7 || p.AppVersion != "v3" {
		t.Fatalf("params metadata not stored: %+v", p)
	}
	// Guard against accidental type drift.
	ty := reflect.TypeOf(Params{})
	for _, name := range []string{"ContentDigest", "DeploymentID", "AppVersion"} {
		if _, ok := ty.FieldByName(name); !ok {
			t.Errorf("Params missing field %s", name)
		}
	}
}

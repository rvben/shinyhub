package cloudflaredemo

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/rvben/shinyhub/deploy/cloudflare-demo/internal/routemanifest"
)

func TestDemoEdgeManifestTracksServerRoutesAssetsAndFleet(t *testing.T) {
	// The generator and normal Go tests run from different working directories.
	t.Chdir("../..")
	t.Setenv("SHINYHUB_DEV_STATIC", "")
	m, err := routemanifest.Build()
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	got, err := os.ReadFile("deploy/cloudflare-demo/src/route-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("demo edge allowlist is stale; run GOWORK=off go run ./deploy/cloudflare-demo/generate-routes")
	}
}

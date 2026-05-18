package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func strptr(s string) *string { return &s }

func TestBuildFleetStatus_SortsAndCounts(t *testing.T) {
	apps := []db.App{
		{Slug: "zeta", Access: "public", Status: "running", ContentDigest: "sha256:abcdef0123", ManagedBy: strptr("fleet:eu")},
		{Slug: "alpha", Access: "private", Status: "stopped", ManagedBy: nil},
		{Slug: "mid", Access: "shared", Status: "running", ManagedBy: strptr("")},
	}
	st := buildFleetStatus("https://h.example", apps)

	if st.SchemaVersion != fleetStatusSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", st.SchemaVersion, fleetStatusSchemaVersion)
	}
	if st.Server != "https://h.example" {
		t.Fatalf("server = %q", st.Server)
	}
	if got := []string{st.Apps[0].Slug, st.Apps[1].Slug, st.Apps[2].Slug}; got[0] != "alpha" || got[1] != "mid" || got[2] != "zeta" {
		t.Fatalf("not slug-sorted: %v", got)
	}
	// Empty-string managed_by is NOT fleet-managed (only a real marker counts).
	if st.Apps[1].FleetManaged || st.Apps[1].ManagedBy != "" {
		t.Fatalf("empty managed_by must be unmanaged: %+v", st.Apps[1])
	}
	zeta := st.Apps[2]
	if !zeta.FleetManaged || zeta.ManagedBy != "fleet:eu" || zeta.ContentDigest != "sha256:abcdef0123" {
		t.Fatalf("zeta row wrong: %+v", zeta)
	}
	if st.Summary.Total != 3 || st.Summary.FleetManaged != 1 || st.Summary.Unmanaged != 2 {
		t.Fatalf("summary wrong: %+v", st.Summary)
	}
}

func TestWriteFleetStatusJSON_StableShape(t *testing.T) {
	st := buildFleetStatus("https://h", []db.App{
		{Slug: "a", Access: "public", Status: "running", ContentDigest: "sha256:dead", ManagedBy: strptr("fleet:eu")},
	})
	var b strings.Builder
	if err := writeFleetStatusJSON(&b, st); err != nil {
		t.Fatalf("writeFleetStatusJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(b.String()), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, b.String())
	}
	if got["schema_version"].(float64) != float64(fleetStatusSchemaVersion) {
		t.Fatalf("schema_version missing/wrong: %v", got["schema_version"])
	}
	apps := got["apps"].([]any)
	row := apps[0].(map[string]any)
	for _, k := range []string{"slug", "managed_by", "fleet_managed", "content_digest", "access", "status"} {
		if _, ok := row[k]; !ok {
			t.Fatalf("app row missing key %q: %v", k, row)
		}
	}
	sum := got["summary"].(map[string]any)
	for _, k := range []string{"total", "fleet_managed", "unmanaged"} {
		if _, ok := sum[k]; !ok {
			t.Fatalf("summary missing key %q: %v", k, sum)
		}
	}
	if ts, ok := got["generated_at"].(string); !ok || ts == "" {
		t.Fatalf("generated_at missing or empty: %v", got["generated_at"])
	}
}

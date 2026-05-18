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

func TestRenderFleetStatus_HumanColumns(t *testing.T) {
	st := buildFleetStatus("https://h.example", []db.App{
		{Slug: "alpha", Access: "private", Status: "running", ManagedBy: nil},
		{Slug: "beta", Access: "public", Status: "running", ContentDigest: "sha256:9f8e7d6c5b4a", ManagedBy: strptr("fleet:eu")},
	})
	var b strings.Builder
	renderFleetStatus(&b, st, false)
	out := b.String()

	if !strings.Contains(out, "server=https://h.example") {
		t.Fatalf("missing server header:\n%s", out)
	}
	if !strings.Contains(out, "Apps (2)") {
		t.Fatalf("missing app count:\n%s", out)
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "unmanaged") {
		t.Fatalf("unmanaged app not shown:\n%s", out)
	}
	if !strings.Contains(out, "beta") || !strings.Contains(out, "fleet:eu") {
		t.Fatalf("managed app/owner not shown:\n%s", out)
	}
	if !strings.Contains(out, "9f8e7d6c") {
		t.Fatalf("short digest not shown:\n%s", out)
	}
	// Exact column alignment + glyphs (the stated design contract).
	if !strings.Contains(out, "  -  alpha  unmanaged  (none)  running") {
		t.Fatalf("unmanaged row glyph/alignment wrong:\n%s", out)
	}
	if !strings.Contains(out, "  *  beta   fleet:eu   9f8e7d6c  running") {
		t.Fatalf("managed row glyph/alignment wrong:\n%s", out)
	}
	// shortDigest must strip the sha256: prefix.
	if strings.Contains(out, "sha256:") {
		t.Fatalf("digest prefix must be stripped:\n%s", out)
	}
	if !strings.Contains(out, "Fleet: 2 app(s), 1 fleet-managed, 1 unmanaged.") {
		t.Fatalf("summary line wrong:\n%s", out)
	}
}

func TestRenderFleetStatus_Quiet(t *testing.T) {
	st := buildFleetStatus("https://h", []db.App{
		{Slug: "a", ManagedBy: strptr("fleet:eu")},
		{Slug: "b", ManagedBy: nil},
	})
	var b strings.Builder
	renderFleetStatus(&b, st, true)
	out := b.String()

	if strings.Contains(out, "Apps (") {
		t.Fatalf("quiet must omit the table:\n%s", out)
	}
	if strings.TrimSpace(out) != "Fleet: 2 app(s), 1 fleet-managed, 1 unmanaged." {
		t.Fatalf("quiet output = %q", out)
	}
}

func TestFleetStatusCmd_ListsManagedAndUnmanaged(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[
		{"slug":"owned-app","access":"public","status":"running","content_digest":"sha256:abc123def456","managed_by":"fleet:eu"},
		{"slug":"loose-app","access":"private","status":"stopped","managed_by":null}
	]`)

	out, err := execCLI(t, "fleet", "status")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "owned-app") || !strings.Contains(out, "fleet:eu") {
		t.Fatalf("managed app missing:\n%s", out)
	}
	if !strings.Contains(out, "loose-app") || !strings.Contains(out, "unmanaged") {
		t.Fatalf("unmanaged app missing:\n%s", out)
	}
	if !strings.Contains(out, "Fleet: 2 app(s), 1 fleet-managed, 1 unmanaged.") {
		t.Fatalf("summary missing:\n%s", out)
	}
}

func TestFleetStatusCmd_JSON(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[{"slug":"a","access":"public","status":"running","managed_by":"fleet:eu"}]`)

	out, err := execCLI(t, "fleet", "status", "--json")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	var env map[string]any
	if jerr := json.Unmarshal([]byte(out), &env); jerr != nil {
		t.Fatalf("not JSON: %v\n%s", jerr, out)
	}
	if env["schema_version"].(float64) != float64(fleetStatusSchemaVersion) {
		t.Fatalf("schema_version wrong: %v", env["schema_version"])
	}
}

func TestFleetStatusCmd_TransportErrorExits3(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(500, `{"error":"boom"}`)

	out, err := execCLI(t, "fleet", "status")
	if err == nil {
		t.Fatalf("expected error on server 500, got nil\n%s", out)
	}
	if code := exitCode(err); code != 3 {
		t.Fatalf("exit code = %d, want 3 (transport)", code)
	}
}

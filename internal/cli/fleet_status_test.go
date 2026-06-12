package cli

import (
	"encoding/json"
	"errors"
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
	items := got["items"].([]any)
	row := items[0].(map[string]any)
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

	out, err := execCLI(t, "fleet", "status", "-o", "table")
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

// FORMAT-3: -o json must produce identical output to --json for fleet status so
// the global flag is a drop-in alias for the command-specific flag.
func TestFleetStatusCmd_OutputJsonEqualsLegacyJson(t *testing.T) {
	const body = `[{"slug":"a","access":"public","status":"running","managed_by":"fleet:eu"}]`

	_, _, setResp := setupCLITest(t)
	setResp(200, body)
	withLegacy, err := execCLI(t, "fleet", "status", "--json")
	if err != nil {
		t.Fatalf("--json: %v", err)
	}

	_, _, setResp2 := setupCLITest(t)
	setResp2(200, body)
	withOutputFlag, err := execCLI(t, "fleet", "status", "-o", "json")
	if err != nil {
		t.Fatalf("-o json: %v", err)
	}

	// Both must parse as valid JSON with the same schema_version. The
	// generated_at timestamp differs between runs, so compare the structural
	// shape rather than the raw string.
	var env1, env2 map[string]any
	if jerr := json.Unmarshal([]byte(withLegacy), &env1); jerr != nil {
		t.Fatalf("--json output not JSON: %v", jerr)
	}
	if jerr := json.Unmarshal([]byte(withOutputFlag), &env2); jerr != nil {
		t.Fatalf("-o json output not JSON: %v", jerr)
	}
	if env1["schema_version"] != env2["schema_version"] {
		t.Errorf("schema_version mismatch: --json=%v, -o json=%v",
			env1["schema_version"], env2["schema_version"])
	}
	if _, ok := env2["items"]; !ok {
		t.Error("-o json output missing items key")
	}
}

// FORMAT-4: -o ndjson is rejected for fleet status (document command, not a stream).
func TestFleetStatusCmd_NdjsonRejected(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[{"slug":"a","access":"public","status":"running","managed_by":"fleet:eu"}]`)

	_, err := execCLI(t, "fleet", "status", "-o", "ndjson")
	if err == nil {
		t.Fatal("want error for -o ndjson on fleet status, got nil")
	}
	if code := exitCode(err); code != 1 {
		t.Errorf("exit code = %d, want 1 (validation)", code)
	}
}

func TestFleetStatus_V2Envelope(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[
		{"slug":"alpha","access":"public","status":"running","managed_by":"fleet:eu"},
		{"slug":"beta","access":"private","status":"stopped","managed_by":null},
		{"slug":"gamma","access":"shared","status":"running","managed_by":"fleet:eu"}
	]`)

	out, err := execCLI(t, "fleet", "status", "--json", "--limit", "1")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	var env map[string]any
	if jerr := json.Unmarshal([]byte(out), &env); jerr != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", jerr, out)
	}
	if env["schema_version"].(float64) != 2 {
		t.Fatalf("schema_version = %v, want 2", env["schema_version"])
	}
	if _, hasApps := env["apps"]; hasApps {
		t.Fatalf("old 'apps' key must be renamed to 'items'")
	}
	items, ok := env["items"].([]any)
	if !ok {
		t.Fatalf("'items' key missing or not an array: %v", env["items"])
	}
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1 (--limit 1)", len(items))
	}
	if env["total"].(float64) != 3 {
		t.Fatalf("total = %v, want 3 (full fleet pre-slice)", env["total"])
	}
	if env["limit"].(float64) != 1 {
		t.Fatalf("limit = %v, want 1", env["limit"])
	}
	if _, ok := env["summary"]; !ok {
		t.Fatalf("summary missing from envelope")
	}
}

// TestFleetStatus_NegativeOffsetValidationError verifies that --offset -1 on
// fleet status (table mode) returns a KindValidation error rather than
// panicking on a negative slice index. The table path previously had its own
// open-coded slice logic that did not guard against negative values.
func TestFleetStatus_NegativeOffsetValidationError(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[{"slug":"a","access":"public","status":"running","managed_by":null}]`)

	_, err := execCLI(t, "fleet", "status", "--offset", "-1")
	if err == nil {
		t.Fatal("want error for --offset -1, got nil")
	}
	var ece *ExitCodeError
	if !errors.As(err, &ece) || ece.Kind != KindValidation {
		t.Errorf("want KindValidation, got %v", err)
	}
}

// TestFleetStatus_NegativeLimitValidationError verifies that --limit -1 on
// fleet status returns a KindValidation error in table mode.
func TestFleetStatus_NegativeLimitValidationError(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[{"slug":"a","access":"public","status":"running","managed_by":null}]`)

	_, err := execCLI(t, "fleet", "status", "--limit", "-1")
	if err == nil {
		t.Fatal("want error for --limit -1, got nil")
	}
	var ece *ExitCodeError
	if !errors.As(err, &ece) || ece.Kind != KindValidation {
		t.Errorf("want KindValidation, got %v", err)
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

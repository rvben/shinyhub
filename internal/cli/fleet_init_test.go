package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/fleet"
)

func hib(v int) *int { return &v }

func initApps() []db.App {
	return []db.App{
		{Slug: "zeta", Access: "public", Replicas: 2, MaxSessionsPerReplica: 50, HibernateTimeoutMinutes: hib(30)},
		{Slug: "alpha", Access: "private"},
	}
}

func TestEmitFleetManifest_SourceRootIsParseClean(t *testing.T) {
	doc := emitFleetManifest("prod-eu", "./apps", initApps())

	// Apps emitted in slug order.
	if strings.Index(doc, `slug       = "alpha"`) > strings.Index(doc, `slug       = "zeta"`) {
		t.Fatalf("apps not slug-ordered:\n%s", doc)
	}
	if !strings.Contains(doc, `fleet_id = "prod-eu"`) {
		t.Fatalf("missing fleet_id:\n%s", doc)
	}
	if !strings.Contains(doc, `source     = "./apps/alpha"`) ||
		!strings.Contains(doc, `source     = "./apps/zeta"`) {
		t.Fatalf("source-root sources missing/active:\n%s", doc)
	}
	if !strings.Contains(doc, `visibility = "public"`) || !strings.Contains(doc, `visibility = "private"`) {
		t.Fatalf("visibility not emitted:\n%s", doc)
	}
	if !strings.Contains(doc, "[app.config]") ||
		!strings.Contains(doc, "hibernate_timeout_minutes = 30") ||
		!strings.Contains(doc, "replicas                  = 2") ||
		!strings.Contains(doc, "max_sessions_per_replica  = 50") {
		t.Fatalf("config block missing:\n%s", doc)
	}
	// The whole point of --source-root: parses with ZERO problems, no edits.
	m, probs := fleet.ParseManifest([]byte(doc), "shinyhub-fleet.toml")
	if len(probs) != 0 {
		t.Fatalf("source-root manifest must parse clean, got %v\n%s", probs, doc)
	}
	if m.FleetID != "prod-eu" || len(m.Apps) != 2 {
		t.Fatalf("parsed manifest wrong: %+v", m)
	}
}

func TestEmitFleetManifest_NoSourceRootIsHonestlyIncomplete(t *testing.T) {
	doc := emitFleetManifest("prod-eu", "", initApps())

	if !strings.Contains(doc, "# source") {
		t.Fatalf("source line must be commented out:\n%s", doc)
	}
	if strings.Contains(doc, "\nsource     =") {
		t.Fatalf("no active source line may be present:\n%s", doc)
	}
	if !strings.Contains(doc, "set each") || !strings.Contains(doc, "remove the leading '#'") {
		t.Fatalf("header must explain the commented-source scaffold:\n%s", doc)
	}
	// Honest scaffold: it parses (no TOML error) but every app fails the
	// existing "source is required" check - a precise message, not a parse
	// error.
	_, probs := fleet.ParseManifest([]byte(doc), "shinyhub-fleet.toml")
	if len(probs) != 2 {
		t.Fatalf("want 2 source-required problems, got %d: %v", len(probs), probs)
	}
	for _, p := range probs {
		if !strings.Contains(p.Msg, "source is required") {
			t.Fatalf("unexpected problem (want 'source is required'): %v", p)
		}
	}
}

// FLT-8: init must annotate apps currently owned by ANOTHER fleet so the
// operator knows a later adopt transfers them. Apps owned by this fleet, or
// unmanaged, get no such comment.
func TestEmitFleetManifest_AnnotatesForeignManagedApps(t *testing.T) {
	apps := []db.App{
		{Slug: "alpha", Access: "private", ManagedBy: strp("fleet:other")},
		{Slug: "beta", Access: "private", ManagedBy: strp("fleet:prod-eu")},
		{Slug: "gamma", Access: "private"},
	}
	doc := emitFleetManifest("prod-eu", "./apps", apps)
	if !strings.Contains(doc, "fleet:other") {
		t.Fatalf("foreign-managed app must be annotated with its current owner:\n%s", doc)
	}
	if strings.Contains(doc, "fleet:prod-eu") {
		t.Fatalf("an app owned by THIS fleet must not be annotated as foreign:\n%s", doc)
	}
	// The annotation must be a TOML comment so the manifest still parses clean.
	if _, probs := fleet.ParseManifest([]byte(doc), "shinyhub-fleet.toml"); len(probs) != 0 {
		t.Fatalf("annotated manifest must parse clean, got %v\n%s", probs, doc)
	}
}

// FLT-6: with zero deployed apps the commented-source scaffold guidance ("set
// each app's source, remove the leading '#'") is nonsense - there are no apps.
// The emitted header must branch on the empty case.
func TestEmitFleetManifest_EmptyAppListGuidance(t *testing.T) {
	doc := emitFleetManifest("prod-eu", "", nil)
	if strings.Contains(doc, "remove the leading '#'") {
		t.Fatalf("empty manifest must not tell the operator to uncomment per-app sources:\n%s", doc)
	}
	if !strings.Contains(doc, "No apps") {
		t.Fatalf("empty manifest should state that no apps are deployed:\n%s", doc)
	}
	if !strings.Contains(doc, `fleet_id = "prod-eu"`) {
		t.Fatalf("fleet_id must still be written:\n%s", doc)
	}
}

func TestTomlString_EscapesQuotesAndBackslashes(t *testing.T) {
	if got := tomlString(`a"b\c`); got != `"a\"b\\c"` {
		t.Fatalf("tomlString = %s", got)
	}
}

func TestFleetInitCmd_WritesManifestWithSourceRoot(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[{"slug":"web","access":"public","replicas":2},{"slug":"api","access":"private"}]`)
	dir := t.TempDir()
	manifest := filepath.Join(dir, "shinyhub-fleet.toml")

	out, err := execCLI(t, "fleet", "init", "--fleet-id", "prod-eu", "--source-root", "./apps", "-f", manifest)
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	data, rerr := os.ReadFile(manifest)
	if rerr != nil {
		t.Fatalf("manifest not written: %v", rerr)
	}
	m, probs := fleet.ParseManifest(data, manifest)
	if len(probs) != 0 {
		t.Fatalf("generated manifest must parse clean: %v\n%s", probs, data)
	}
	if m.FleetID != "prod-eu" || len(m.Apps) != 2 {
		t.Fatalf("parsed manifest wrong: %+v", m)
	}
	if !strings.Contains(out, "fleet_id=prod-eu") || !strings.Contains(out, "2 app(s)") {
		t.Fatalf("summary missing:\n%s", out)
	}
}

// FLT-6: the command's post-write guidance must also branch on the empty case
// instead of telling the operator to set per-app sources that do not exist.
func TestFleetInitCmd_EmptyAppListGuidance(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[]`)
	dir := t.TempDir()
	out, err := execCLI(t, "fleet", "init", "--fleet-id", "prod-eu", "-f", filepath.Join(dir, "shinyhub-fleet.toml"))
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if strings.Contains(out, "source path for every") || strings.Contains(out, "remove the leading '#'") {
		t.Fatalf("0-app init must not reference per-app sources:\n%s", out)
	}
	if !strings.Contains(out, "0 app(s)") {
		t.Fatalf("summary should report 0 app(s):\n%s", out)
	}
	if !strings.Contains(out, "[[app]]") {
		t.Fatalf("0-app guidance should point at adding [[app]] blocks:\n%s", out)
	}
}

func TestFleetInitCmd_NonTTYWithoutFleetIDFailsExit1(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[]`)
	dir := t.TempDir()

	out, err := execCLI(t, "fleet", "init", "-f", filepath.Join(dir, "shinyhub-fleet.toml"))
	if err == nil {
		t.Fatalf("expected error without --fleet-id in non-TTY, got nil\n%s", out)
	}
	if code := exitCode(err); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(err.Error(), "--fleet-id is required") {
		t.Fatalf("error should name --fleet-id: %v", err)
	}
}

func TestFleetInitCmd_PromptsForFleetIDWhenTTY(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[{"slug":"only","access":"private"}]`)
	prev := isStdinTTY
	isStdinTTY = func() bool { return true }
	t.Cleanup(func() { isStdinTTY = prev })
	dir := t.TempDir()
	manifest := filepath.Join(dir, "shinyhub-fleet.toml")

	out, err := execCLIStdin(t, strings.NewReader("staging-eu\n"),
		"fleet", "init", "--source-root", "./apps", "-f", manifest)
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	data, _ := os.ReadFile(manifest)
	if !strings.Contains(string(data), `fleet_id = "staging-eu"`) {
		t.Fatalf("prompted fleet_id not used:\n%s", data)
	}
}

func TestFleetInitCmd_RejectsInvalidFleetID(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[]`)
	dir := t.TempDir()

	out, err := execCLI(t, "fleet", "init", "--fleet-id", "Prod_EU", "-f", filepath.Join(dir, "shinyhub-fleet.toml"))
	if err == nil {
		t.Fatalf("expected error for invalid fleet_id, got nil\n%s", out)
	}
	if code := exitCode(err); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

func TestFleetInitCmd_RefusesOverwriteWithoutForce(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[]`)
	dir := t.TempDir()
	manifest := filepath.Join(dir, "shinyhub-fleet.toml")
	if werr := os.WriteFile(manifest, []byte("existing"), 0o644); werr != nil {
		t.Fatal(werr)
	}

	_, err := execCLI(t, "fleet", "init", "--fleet-id", "eu", "-f", manifest)
	if err == nil {
		t.Fatal("expected error overwriting existing file without --force")
	}
	if code := exitCode(err); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if data, _ := os.ReadFile(manifest); string(data) != "existing" {
		t.Fatalf("file must be untouched without --force, got %q", data)
	}

	out2, err2 := execCLI(t, "fleet", "init", "--fleet-id", "eu", "-f", manifest, "--force")
	if err2 != nil {
		t.Fatalf("--force should overwrite: %v\n%s", err2, out2)
	}
	if data, _ := os.ReadFile(manifest); string(data) == "existing" {
		t.Fatal("--force did not overwrite the file")
	}
}

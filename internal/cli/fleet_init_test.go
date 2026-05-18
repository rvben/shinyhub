package cli

import (
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

func TestTomlString_EscapesQuotesAndBackslashes(t *testing.T) {
	if got := tomlString(`a"b\c`); got != `"a\"b\\c"` {
		t.Fatalf("tomlString = %s", got)
	}
}

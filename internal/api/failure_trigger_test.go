package api

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

type dbFailureTrigger struct {
	name      string
	table     string
	event     string
	condition string
}

var testSQLIdentifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// installDBFailureTrigger installs the same statement-failure checkpoint on
// both supported databases. The activation recovery tests deliberately break
// durable writes after a process has started; keeping that fault injection
// portable ensures PostgreSQL exercises the same crash boundaries as SQLite.
//
// The returned cleanup is idempotent so a test can remove the fault before a
// repair retry while still retaining t.Cleanup protection on an early failure.
func installDBFailureTrigger(t *testing.T, store *db.Store, spec dbFailureTrigger) func() {
	t.Helper()
	for label, identifier := range map[string]string{
		"trigger": spec.name,
		"table":   spec.table,
	} {
		if !testSQLIdentifier.MatchString(identifier) {
			t.Fatalf("invalid test %s identifier %q", label, identifier)
		}
	}
	switch spec.event {
	case "INSERT", "UPDATE", "UPDATE OF phase", "DELETE":
	default:
		t.Fatalf("unsupported test trigger event %q", spec.event)
	}
	if spec.condition == "" {
		t.Fatal("test trigger condition must not be empty")
	}

	functionName := spec.name + "_fn"
	if store.IsPostgres() {
		if _, err := store.DB().Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger AS $failure$
			BEGIN RAISE EXCEPTION 'injected checkpoint failure'; END;
			$failure$ LANGUAGE plpgsql`, functionName)); err != nil {
			t.Fatalf("create PostgreSQL failure function: %v", err)
		}
		if _, err := store.DB().Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE %s ON %s
			FOR EACH ROW WHEN (%s) EXECUTE FUNCTION %s()`,
			spec.name, spec.event, spec.table, spec.condition, functionName)); err != nil {
			_, _ = store.DB().Exec(fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
			t.Fatalf("create PostgreSQL failure trigger: %v", err)
		}
	} else {
		if _, err := store.DB().Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE %s ON %s
			WHEN %s BEGIN SELECT RAISE(FAIL, 'injected checkpoint failure'); END`,
			spec.name, spec.event, spec.table, spec.condition)); err != nil {
			t.Fatalf("create SQLite failure trigger: %v", err)
		}
	}

	dropped := false
	drop := func() {
		t.Helper()
		if dropped {
			return
		}
		dropped = true
		if store.IsPostgres() {
			if _, err := store.DB().Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON %s`, spec.name, spec.table)); err != nil {
				t.Errorf("drop PostgreSQL failure trigger: %v", err)
			}
			if _, err := store.DB().Exec(fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName)); err != nil {
				t.Errorf("drop PostgreSQL failure function: %v", err)
			}
			return
		}
		if _, err := store.DB().Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s`, spec.name)); err != nil {
			t.Errorf("drop SQLite failure trigger: %v", err)
		}
	}
	t.Cleanup(drop)
	return drop
}

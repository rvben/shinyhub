package worker_test

import (
	"bytes"
	"testing"

	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/worker"
)

// TestLoadOrInitCA_AgainstDBStore verifies that *db.Store satisfies worker.CAStore
// and that LoadOrInitCA works end-to-end: the CA is stable across reloads (decrypt
// round-trip through the DB) and a wrong auth secret fails loudly.
// Runs on SQLite by default; Postgres when SHINYHUB_TEST_POSTGRES_DSN is set.
func TestLoadOrInitCA_AgainstDBStore(t *testing.T) {
	store := dbtest.New(t)
	ca, err := worker.LoadOrInitCA(store, t.TempDir(), "integration-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	// A second load reads the persisted CA back (decrypt round-trip through the DB).
	ca2, err := worker.LoadOrInitCA(store, t.TempDir(), "integration-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ca.CertPEM(), ca2.CertPEM()) {
		t.Fatal("CA not stable across reloads from the DB")
	}
	// Wrong secret on reload fails loudly.
	if _, err := worker.LoadOrInitCA(store, t.TempDir(), "wrong-secret", nil); err == nil {
		t.Fatal("wrong secret must fail")
	}
}

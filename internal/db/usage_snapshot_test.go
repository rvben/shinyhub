package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type usageRetentionInterleave struct {
	usageReportQueryer
	queries int
	prune   func() error
}

func (q *usageRetentionInterleave) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	q.queries++
	if q.queries == 2 {
		// Raw totals have already been read. Commit retention before the peak
		// and rollup queries, reproducing the double-counting interleaving.
		if err := q.prune(); err != nil {
			return nil, err
		}
	}
	return q.usageReportQueryer.QueryContext(ctx, query, args...)
}

func TestUsageReportSnapshotSurvivesRetention(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "snapshot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser(CreateUserParams{Username: "snapshot-owner", PasswordHash: "h", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, err := store.GetUserByUsername("snapshot-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(CreateAppParams{Slug: "snapshot", Name: "Snapshot", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<20)
		INSERT INTO usage_sessions (id, app_id, principal_kind, identity_mode, instance_id, started_at, heartbeat_at, ended_at)
		SELECT CAST(n AS TEXT), ?, 'anonymous', 'unattributed', 'test',
		datetime('now', '-100 days'), datetime('now', '-100 days', '+1 hour'),
		datetime('now', '-100 days', '+1 hour') FROM seq`, app.ID); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	before, err := store.AppUsageReport(ctx, app.ID, 120*24*time.Hour, "unattributed", false)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.real.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	q := &usageRetentionInterleave{usageReportQueryer: &boundTx{tx: tx, d: store.d}, prune: func() error {
		n, err := store.PruneUsageSessions(90 * 24 * time.Hour)
		if err == nil && n != 20 {
			t.Fatalf("pruned %d rows, want 20", n)
		}
		return err
	}}
	during, err := store.appUsageReport(ctx, q, app.ID, 120*24*time.Hour, "unattributed", false)
	if err != nil {
		t.Fatal(err)
	}
	if q.queries < 2 {
		t.Fatal("retention interleaving was not exercised")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after, err := store.AppUsageReport(ctx, app.ID, 120*24*time.Hour, "unattributed", false)
	if err != nil {
		t.Fatal(err)
	}
	if before.Summary.Sessions != 20 || before.Summary.TotalDurationSeconds != 72000 ||
		!reflect.DeepEqual(before, during) || !reflect.DeepEqual(before, after) {
		t.Fatalf("retention changed the report: before=%+v during=%+v after=%+v", before, during, after)
	}
}

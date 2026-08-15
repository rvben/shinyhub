package db_test

import (
	"context"
	"database/sql"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/logstream"
)

type postgresLogFanoutMetrics struct {
	followers    atomic.Int64
	viewers      atomic.Int64
	followErrors atomic.Int64
}

func (*postgresLogFanoutMetrics) RecordAppLogFlush(string, time.Duration, time.Duration) {}
func (*postgresLogFanoutMetrics) AddAppLogPendingBytes(int64)                            {}
func (*postgresLogFanoutMetrics) RecordAppLogDroppedBytes(int64)                         {}
func (m *postgresLogFanoutMetrics) AddAppLogFollowers(delta int) {
	m.followers.Add(int64(delta))
}
func (m *postgresLogFanoutMetrics) AddAppLogViewers(delta int) {
	m.viewers.Add(int64(delta))
}
func (m *postgresLogFanoutMetrics) RecordAppLogFollowError() {
	m.followErrors.Add(1)
}

// TestPostgresAppLogFanoutRecoversAcrossStores exercises the HA path against a
// real Postgres database. One independently pooled store follows while another
// writes. An exclusive table lock makes the follower's short statement timeout
// fail for real, after which it must report degradation, recover, and resume at
// the exact durable byte cursor without replaying the snapshot.
func TestPostgresAppLogFanoutRecoversAcrossStores(t *testing.T) {
	writerStore, dsn := dbtest.NewPostgres(t)
	separator := "?"
	if strings.ContainsRune(dsn, '?') {
		separator = "&"
	}
	readerStore, err := db.Open(dsn + separator + "statement_timeout=75")
	if err != nil {
		t.Fatalf("open independent follower store: %v", err)
	}
	t.Cleanup(func() { _ = readerStore.Close() })
	lockDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open lock connection: %v", err)
	}
	t.Cleanup(func() { _ = lockDB.Close() })

	metrics := &postgresLogFanoutMetrics{}
	readerStore.SetAppLogMetrics(metrics)
	if err := writerStore.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "hash", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, err := writerStore.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writerStore.CreateApp(db.CreateAppParams{Slug: "fanout", Name: "Fanout", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	app, err := writerStore.GetAppBySlug("fanout")
	if err != nil {
		t.Fatal(err)
	}
	const runID = "88888888-8888-4888-8888-888888888888"
	if err := writerStore.CreateAppLogRun(db.CreateAppLogRunParams{
		RunID: runID, AppID: app.ID, ReplicaIndex: 1, Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	before := []byte("before outage\n")
	if err := writerStore.AppendAppLogChunk(runID, 0, 0, before, db.AppLogRetentionBytes, time.Now()); err != nil {
		t.Fatal(err)
	}

	reader := readerStore.NewAppLogReader(runID)
	snapshot, cursor, err := reader.SnapshotTail(200)
	if err != nil || len(snapshot) != 1 || snapshot[0].Line != "before outage" || cursor != int64(len(before)) {
		t.Fatalf("snapshot = %+v cursor=%d err=%v", snapshot, cursor, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	records := make(chan logstream.Record, 16)
	go reader.FollowFrom(ctx, cursor, records)
	waitForPostgresLogMetric(t, "follower subscription", func() bool {
		return metrics.followers.Load() == 1 && metrics.viewers.Load() == 1
	})

	lockTx, err := lockDB.Begin()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	locked := true
	t.Cleanup(func() {
		if locked {
			_ = lockTx.Rollback()
		}
	})
	if _, err := lockTx.Exec(`LOCK TABLE app_log_chunks IN ACCESS EXCLUSIVE MODE`); err != nil {
		cancel()
		t.Fatalf("lock app-log table: %v", err)
	}

	degraded := receivePostgresLogRecord(t, records, "degraded state")
	if degraded.StreamState != logstream.StreamDegraded {
		cancel()
		t.Fatalf("outage record = %+v, want degraded state", degraded)
	}
	if metrics.followErrors.Load() == 0 {
		cancel()
		t.Fatal("real Postgres follower failure was not recorded")
	}
	if err := lockTx.Commit(); err != nil {
		cancel()
		t.Fatalf("release app-log table lock: %v", err)
	}
	locked = false

	during := []byte("written from other store\n")
	if err := writerStore.AppendAppLogChunk(runID, 1, cursor, during, db.AppLogRetentionBytes, time.Now()); err != nil {
		cancel()
		t.Fatal(err)
	}
	recovered := receivePostgresLogRecord(t, records, "recovered state")
	if recovered.StreamState != logstream.StreamRecovered {
		cancel()
		t.Fatalf("recovery record = %+v, want recovered state", recovered)
	}
	line := receivePostgresLogRecord(t, records, "cross-store output")
	wantEnd := cursor + int64(len(during))
	if line.StreamState != "" || line.Line != "written from other store" || line.EndOffset != wantEnd || line.GapBefore {
		cancel()
		t.Fatalf("resumed line = %+v, want exact cursor %d without gap", line, wantEnd)
	}
	select {
	case duplicate := <-records:
		cancel()
		t.Fatalf("follower replayed or duplicated output: %+v", duplicate)
	case <-time.After(600 * time.Millisecond):
	}

	cancel()
	waitForPostgresLogMetric(t, "follower cleanup", func() bool {
		return metrics.followers.Load() == 0 && metrics.viewers.Load() == 0
	})
}

func receivePostgresLogRecord(t *testing.T, records <-chan logstream.Record, description string) logstream.Record {
	t.Helper()
	select {
	case record := <-records:
		return record
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return logstream.Record{}
	}
}

func waitForPostgresLogMetric(t *testing.T, description string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

var _ db.AppLogMetrics = (*postgresLogFanoutMetrics)(nil)

package db_test

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// BenchmarkAppendAppLogChunk measures one shared-log append at the retention
// cap, where every append also has to trim the oldest bytes. The chunk size
// decides how many rows the cap holds: the writer's full 33 KiB chunks keep
// about 160 rows, while an app that flushes 200 B lines keeps about 26k. The
// cost per append must not depend on that row count. Run with:
//
//	go test ./internal/db -run '^$' -bench AppendAppLogChunk -benchmem -benchtime=50x
func BenchmarkAppendAppLogChunk(b *testing.B) {
	dbtest.SkipIfPostgres(b)
	for _, chunkBytes := range []int{33 << 10, 4096, 200} {
		b.Run(fmt.Sprintf("chunk-%dB", chunkBytes), func(b *testing.B) {
			dsn := filepath.Join(b.TempDir(), "shinyhub.db")
			dbtest.WriteSQLiteFile(b, dsn)
			store, err := db.Open(dsn)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { store.Close() })
			runID := newLogRun(b, store)
			data := bytes.Repeat([]byte("x"), chunkBytes)
			// Fill past the cap so every measured append trims as well.
			var seq, offset int64
			for offset <= db.AppLogRetentionBytes {
				if err := store.AppendAppLogChunk(runID, seq, offset, data, db.AppLogRetentionBytes, time.Now()); err != nil {
					b.Fatal(err)
				}
				seq++
				offset += int64(chunkBytes)
			}
			b.ReportAllocs()
			for b.Loop() {
				if err := store.AppendAppLogChunk(runID, seq, offset, data, db.AppLogRetentionBytes, time.Now()); err != nil {
					b.Fatal(err)
				}
				seq++
				offset += int64(chunkBytes)
			}
		})
	}
}

// newLogRun creates an owner, an app and one running app log run, and returns
// the run ID.
func newLogRun(tb testing.TB, store *db.Store) string {
	tb.Helper()
	if err := store.CreateUser(db.CreateUserParams{Username: "log-owner", PasswordHash: "h", Role: "developer"}); err != nil {
		tb.Fatal(err)
	}
	owner, err := store.GetUserByUsername("log-owner")
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "log-app", Name: "Log App", OwnerID: owner.ID}); err != nil {
		tb.Fatal(err)
	}
	app, err := store.GetAppBySlug("log-app")
	if err != nil {
		tb.Fatal(err)
	}
	runID := "77777777-7777-4777-8777-777777777777"
	if err := store.CreateAppLogRun(db.CreateAppLogRunParams{
		RunID: runID, AppID: app.ID, Status: "running", StartedAt: time.Now(),
	}); err != nil {
		tb.Fatal(err)
	}
	return runID
}

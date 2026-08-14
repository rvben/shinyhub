package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestAppLogRunsRoundTripAndOrder(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "hash", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.GetUserByUsername("owner")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("demo")
	older := time.Unix(1_700_000_000, 0)
	newer := older.Add(time.Minute)
	for _, p := range []db.CreateAppLogRunParams{
		{RunID: "11111111-1111-4111-8111-111111111111", AppID: app.ID, ReplicaIndex: 0, AppVersion: "v1", Tier: "local", Status: "starting", StartedAt: older},
		{RunID: "22222222-2222-4222-8222-222222222222", AppID: app.ID, ReplicaIndex: 0, AppVersion: "v2", Tier: "burst", Status: "starting", StartedAt: newer},
	} {
		if err := store.CreateAppLogRun(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.MarkAppLogRunRunning("22222222-2222-4222-8222-222222222222", "remote_docker"); err != nil {
		t.Fatal(err)
	}
	finished := older.Add(30 * time.Second)
	if err := store.FinishAppLogRun("11111111-1111-4111-8111-111111111111", "crashed", finished, true); err != nil {
		t.Fatal(err)
	}

	runs, err := store.ListAppLogRuns(app.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].AppVersion != "v2" || runs[1].AppVersion != "v1" {
		t.Fatalf("runs = %+v", runs)
	}
	if runs[0].Status != "running" || runs[0].Provider != "remote_docker" {
		t.Errorf("new run = %+v", runs[0])
	}
	if runs[1].Status != "crashed" || runs[1].FinishedAt == nil || !runs[1].OOMKilled {
		t.Errorf("old run = %+v", runs[1])
	}
	got, err := store.GetAppLogRun(app.ID, runs[1].RunID)
	if err != nil || got.RunID != runs[1].RunID {
		t.Fatalf("GetAppLogRun = %+v, %v", got, err)
	}
	if _, err := store.GetAppLogRun(app.ID+100, runs[1].RunID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("cross-app lookup err = %v, want ErrNotFound", err)
	}
}

func TestAppLogChunksRetainNewestBytesAndMonotonicOffsets(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "hash", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.GetUserByUsername("owner")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("demo")
	runID := "55555555-5555-4555-8555-555555555555"
	if err := store.CreateAppLogRun(db.CreateAppLogRunParams{
		RunID: runID, AppID: app.ID, Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	for i, part := range []string{"abcd", "efgh", "ijkl"} {
		if err := store.AppendAppLogChunk(runID, int64(i), int64(i*4), []byte(part), 8, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.ReadAppLog(runID)
	if err != nil || string(got) != "efghijkl" {
		t.Fatalf("ReadAppLog = %q, %v", got, err)
	}
	from, end, err := store.ReadAppLogFrom(runID, 0)
	if err != nil || string(from) != "efghijkl" || end != 12 {
		t.Fatalf("ReadAppLogFrom = %q, end=%d, err=%v", from, end, err)
	}
	stats, err := store.AppLogStatsForRuns([]string{runID})
	if err != nil || stats[runID].SizeBytes != 8 || stats[runID].UpdatedAt.IsZero() {
		t.Fatalf("stats = %+v, %v", stats, err)
	}
}

func TestAppLogWriterAndReaderStreamSharedOutput(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "hash", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.GetUserByUsername("owner")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("demo")
	runID := "66666666-6666-4666-8666-666666666666"
	if err := store.CreateAppLogRun(db.CreateAppLogRunParams{
		RunID: runID, AppID: app.ID, Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	reader := store.NewAppLogReader(runID)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lines := make(chan string, 1)
	go reader.Follow(ctx, lines)
	time.Sleep(50 * time.Millisecond)
	writer, err := store.NewAppLogWriter(runID, db.AppLogRetentionBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("first\nsecond\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-lines:
		if got != "first" {
			t.Fatalf("follow line = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for shared live log")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	tail, err := reader.Tail(1)
	if err != nil || len(tail) != 1 || tail[0] != "second" {
		t.Fatalf("Tail = %v, %v", tail, err)
	}
}

func TestCreateAppLogRunSupersedesUnfinishedSlotRun(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "hash", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.GetUserByUsername("owner")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("demo")
	first := db.CreateAppLogRunParams{RunID: "33333333-3333-4333-8333-333333333333", AppID: app.ID, ReplicaIndex: 2, Status: "running", StartedAt: time.Unix(1_700_000_000, 0)}
	second := db.CreateAppLogRunParams{RunID: "44444444-4444-4444-8444-444444444444", AppID: app.ID, ReplicaIndex: 2, Status: "starting", StartedAt: first.StartedAt.Add(time.Second)}
	if err := store.CreateAppLogRun(first); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAppLogRun(second); err != nil {
		t.Fatal(err)
	}
	run, err := store.GetAppLogRun(app.ID, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "interrupted" || run.FinishedAt == nil || !run.FinishedAt.Equal(second.StartedAt) {
		t.Fatalf("superseded run = %+v", run)
	}
}

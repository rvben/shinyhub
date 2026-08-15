package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/logstream"
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
	window, start, end, err := store.ReadAppLogWindow(runID, 0)
	if err != nil || string(window) != "efghijkl" || start != 4 || end != 12 {
		t.Fatalf("ReadAppLogWindow = %q, start=%d, end=%d, err=%v", window, start, end, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resumed := make(chan logstream.Record, 1)
	go store.NewAppLogReader(runID).FollowFrom(ctx, 0, resumed)
	select {
	case record := <-resumed:
		if record.Line != "efghijkl" || record.EndOffset != 12 || !record.GapBefore {
			t.Fatalf("retention-gap record = %+v", record)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for retained resume window")
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

func TestAppLogReaderSnapshotCursorClosesTailFollowGap(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "hash", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.GetUserByUsername("owner")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("demo")
	const runID = "77777777-7777-4777-8777-777777777777"
	if err := store.CreateAppLogRun(db.CreateAppLogRunParams{
		RunID: runID, AppID: app.ID, Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendAppLogChunk(runID, 0, 0, []byte("before\n"), db.AppLogRetentionBytes, time.Now()); err != nil {
		t.Fatal(err)
	}

	reader := store.NewAppLogReader(runID)
	records, cursor, err := reader.SnapshotTail(200)
	if err != nil || len(records) != 1 || records[0].Line != "before" || cursor != int64(len("before\n")) {
		t.Fatalf("snapshot = %+v cursor=%d err=%v", records, cursor, err)
	}
	if err := store.AppendAppLogChunk(runID, 1, cursor, []byte("written during handoff\n"), db.AppLogRetentionBytes, time.Now()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch := make(chan logstream.Record, 2)
	go reader.FollowFrom(ctx, cursor, ch)
	select {
	case got := <-ch:
		if got.Line != "written during handoff" || got.EndOffset != int64(len("before\nwritten during handoff\n")) {
			t.Fatalf("followed record = %+v", got)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for shared handoff output")
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

func TestPruneAppLogRunsKeepsNewestPerReplicaAndCascadesChunks(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "hash", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.GetUserByUsername("owner")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("demo")
	base := time.Unix(1_700_000_000, 0)
	replicaZero := []string{
		"80000000-0000-4000-8000-000000000001",
		"80000000-0000-4000-8000-000000000002",
		"80000000-0000-4000-8000-000000000003",
		"80000000-0000-4000-8000-000000000004",
	}
	replicaOne := []string{
		"81000000-0000-4000-8000-000000000001",
		"81000000-0000-4000-8000-000000000002",
	}
	create := func(runID string, replica, minute int, leaveRunning bool) {
		t.Helper()
		started := base.Add(time.Duration(minute) * time.Minute)
		if err := store.CreateAppLogRun(db.CreateAppLogRunParams{
			RunID: runID, AppID: app.ID, ReplicaIndex: replica, Status: "starting", StartedAt: started,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkAppLogRunRunning(runID, "remote_docker"); err != nil {
			t.Fatal(err)
		}
		if err := store.AppendAppLogChunk(runID, 0, 0, []byte(runID), db.AppLogRetentionBytes, started); err != nil {
			t.Fatal(err)
		}
		if !leaveRunning {
			if err := store.FinishAppLogRun(runID, "stopped", started.Add(time.Second), false); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i, runID := range replicaZero {
		create(runID, 0, i, i == len(replicaZero)-1)
	}
	for i, runID := range replicaOne {
		create(runID, 1, i, false)
	}
	if n, err := store.PruneAppLogRuns(0); err != nil || n != 0 {
		t.Fatalf("disabled prune = %d, %v", n, err)
	}
	deleted, err := store.PruneAppLogRuns(2)
	if err != nil || deleted != 2 {
		t.Fatalf("PruneAppLogRuns = %d, %v, want 2", deleted, err)
	}
	runs, err := store.ListAppLogRuns(app.ID, 100)
	if err != nil || len(runs) != 4 {
		t.Fatalf("remaining runs = %d, %v", len(runs), err)
	}
	ids, err := store.ListAppLogRunIDs()
	if err != nil {
		t.Fatal(err)
	}
	for _, runID := range replicaZero[:2] {
		if _, ok := ids[runID]; ok {
			t.Errorf("pruned run %s still retained", runID)
		}
		if _, exists, err := store.AppLogEndOffset(runID); err != nil || exists {
			t.Errorf("pruned chunks for %s: exists=%v err=%v", runID, exists, err)
		}
	}
	for _, runID := range append(replicaZero[2:], replicaOne...) {
		if _, ok := ids[runID]; !ok {
			t.Errorf("expected retained run %s", runID)
		}
		if _, exists, err := store.AppLogEndOffset(runID); err != nil || !exists {
			t.Errorf("retained chunks for %s: exists=%v err=%v", runID, exists, err)
		}
	}
}

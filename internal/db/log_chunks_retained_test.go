package db_test

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/dbtest"
)

// The run row carries the retained size of its shared output and every append
// keeps it in step, through whole-chunk deletes, an in-place trim and a
// redelivered chunk, so staying within the cap never has to sum the chunks.
func TestAppendAppLogChunk_KeepsRetainedBytesOnRun(t *testing.T) {
	store := dbtest.New(t)
	runID := newLogRun(t, store)

	retained := func() (counter, summed int64) {
		t.Helper()
		if err := store.DB().QueryRow(`SELECT bytes_retained FROM app_log_runs WHERE run_id = ?`, runID).Scan(&counter); err != nil {
			t.Fatalf("read bytes_retained: %v", err)
		}
		if err := store.DB().QueryRow(`SELECT COALESCE(SUM(end_offset - start_offset), 0) FROM app_log_chunks WHERE run_id = ?`, runID).Scan(&summed); err != nil {
			t.Fatalf("sum chunks: %v", err)
		}
		return counter, summed
	}

	steps := []struct {
		name       string
		seq, start int64
		data       string
		want       int64
	}{
		{"first chunk", 0, 0, "abcd", 4},
		{"fills the cap", 1, 4, "efgh", 8},
		{"deletes the oldest chunk whole", 2, 8, "ijkl", 8},
		{"ignores a redelivered chunk", 2, 8, "ijkl", 8},
		{"deletes one chunk and trims the next in place", 3, 12, "mnopqr", 8},
	}
	for _, step := range steps {
		if err := store.AppendAppLogChunk(runID, step.seq, step.start, []byte(step.data), 8, time.Now()); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		counter, summed := retained()
		if counter != step.want || summed != step.want {
			t.Fatalf("%s: bytes_retained = %d, chunks sum to %d, want both %d", step.name, counter, summed, step.want)
		}
	}
	if got, err := store.ReadAppLog(runID); err != nil || string(got) != "klmnopqr" {
		t.Fatalf("ReadAppLog = %q, %v; want the newest 8 bytes", got, err)
	}
}

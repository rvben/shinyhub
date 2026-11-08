package db

import "fmt"

// WALCheckpoint reports the state of the write-ahead log after a checkpoint.
// A completed TRUNCATE checkpoint leaves nothing behind, so SQLite reports it
// as zero frames; the fields only carry information when the checkpoint was
// held back.
type WALCheckpoint struct {
	// Blocked is set when a reader or writer kept the checkpoint from
	// completing within busy_timeout. The log is left as it was and the next
	// pass tries again.
	Blocked bool
	// PendingFrames is the number of frames still waiting in the log to be
	// copied into the database file; 0 unless Blocked.
	PendingFrames int
}

// IsSQLiteFile reports whether the store is a file-backed SQLite database,
// the only backend with a write-ahead log and planner statistics that this
// process has to maintain itself.
func (s *Store) IsSQLiteFile() bool {
	_, isSQLite := s.d.(sqliteDialect)
	return isSQLite && !s.memory
}

// CheckpointWAL copies the write-ahead log into the database file and
// truncates the log to zero bytes. The automatic checkpoint only resets the
// log's write position, so without this the file keeps the high-water mark
// of its busiest hour forever. Stores without a WAL file (Postgres and
// in-memory SQLite) return the zero result.
func (s *Store) CheckpointWAL() (WALCheckpoint, error) {
	if !s.IsSQLiteFile() {
		return WALCheckpoint{}, nil
	}
	defer s.timed("CheckpointWAL")()
	var busy, frames, copied int
	if err := s.rawDB().QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &copied); err != nil {
		return WALCheckpoint{}, fmt.Errorf("wal_checkpoint: %w", err)
	}
	return WALCheckpoint{Blocked: busy != 0, PendingFrames: frames - copied}, nil
}

// Optimize refreshes the query planner's statistics for the tables the
// connection it runs on has queried and whose statistics are missing or stale.
// It is cheap enough to run on every maintenance pass and on Close, which is
// where SQLite recommends it for long-lived connections. Stores without a
// SQLite file are a no-op.
func (s *Store) Optimize() error {
	if !s.IsSQLiteFile() {
		return nil
	}
	defer s.timed("Optimize")()
	if _, err := s.rawDB().Exec("PRAGMA optimize"); err != nil {
		return fmt.Errorf("optimize: %w", err)
	}
	return nil
}

package db

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/logstream"
)

const (
	// AppLogRetentionBytes matches the local per-run viewer cap. The database
	// keeps the newest bytes, not the first bytes, so crash output survives a
	// noisy long-running process.
	AppLogRetentionBytes int64 = 5 << 20
	appLogChunkBytes           = 64 << 10
	appLogFlushInterval        = 200 * time.Millisecond
)

// AppLogMetrics receives process-local shared-log pipeline signals without
// coupling database persistence to Prometheus. Implementations must be safe for
// concurrent use by multiple app-log writers.
type AppLogMetrics interface {
	RecordAppLogFlush(result string, duration, persistenceLag time.Duration)
	AddAppLogPendingBytes(delta int64)
	RecordAppLogDroppedBytes(bytes int64)
}

// AppLogStats describes the retained shared output for one immutable run.
type AppLogStats struct {
	SizeBytes int64
	UpdatedAt time.Time
}

// AppendAppLogChunk atomically adds one chunk and trims the oldest bytes until
// the run is within maxBytes. Offsets are never renumbered during trimming,
// which lets followers use one monotonic cursor for the lifetime of a run.
func (s *Store) AppendAppLogChunk(runID string, seq, startOffset int64, data []byte, maxBytes int64, createdAt time.Time) error {
	defer s.timed("AppendAppLogChunk")()
	if runID == "" || len(data) == 0 {
		return nil
	}
	if maxBytes <= 0 {
		maxBytes = AppLogRetentionBytes
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("append app log chunk begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	endOffset := startOffset + int64(len(data))
	if _, err := tx.Exec(`
		INSERT INTO app_log_chunks
			(run_id, chunk_seq, start_offset, end_offset, data, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (run_id, chunk_seq) DO NOTHING`,
		runID, seq, startOffset, endOffset, data, createdAt.UnixMilli()); err != nil {
		return fmt.Errorf("append app log chunk: %w", err)
	}

	var total int64
	if err := tx.QueryRow(`
		SELECT COALESCE(SUM(end_offset - start_offset), 0)
		FROM app_log_chunks WHERE run_id = ?`, runID).Scan(&total); err != nil {
		return fmt.Errorf("size app log chunks: %w", err)
	}
	for total > maxBytes {
		var oldestSeq, oldestStart int64
		var oldest []byte
		if err := tx.QueryRow(`
			SELECT chunk_seq, start_offset, data
			FROM app_log_chunks WHERE run_id = ?
			ORDER BY chunk_seq LIMIT 1`, runID).Scan(&oldestSeq, &oldestStart, &oldest); err != nil {
			return fmt.Errorf("read oldest app log chunk: %w", err)
		}
		drop := total - maxBytes
		if int64(len(oldest)) <= drop {
			if _, err := tx.Exec(`DELETE FROM app_log_chunks WHERE run_id = ? AND chunk_seq = ?`, runID, oldestSeq); err != nil {
				return fmt.Errorf("prune app log chunk: %w", err)
			}
			total -= int64(len(oldest))
			continue
		}
		trimmed := append([]byte(nil), oldest[drop:]...)
		if _, err := tx.Exec(`
			UPDATE app_log_chunks SET start_offset = ?, data = ?
			WHERE run_id = ? AND chunk_seq = ?`,
			oldestStart+drop, trimmed, runID, oldestSeq); err != nil {
			return fmt.Errorf("trim app log chunk: %w", err)
		}
		total -= drop
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("append app log chunk commit: %w", err)
	}
	return nil
}

// ReadAppLog returns all currently retained bytes in chronological order.
func (s *Store) ReadAppLog(runID string) ([]byte, error) {
	defer s.timed("ReadAppLog")()
	rows, err := s.db.Query(`
		SELECT data FROM app_log_chunks
		WHERE run_id = ? ORDER BY chunk_seq`, runID)
	if err != nil {
		return nil, fmt.Errorf("read app log: %w", err)
	}
	defer rows.Close()
	var out bytes.Buffer
	for rows.Next() {
		var chunk []byte
		if err := rows.Scan(&chunk); err != nil {
			return nil, fmt.Errorf("scan app log chunk: %w", err)
		}
		_, _ = out.Write(chunk)
	}
	return out.Bytes(), rows.Err()
}

// ReadAppLogFrom returns retained bytes after offset and the new monotonic end
// cursor. If retention has advanced beyond offset, it starts at the earliest
// still-retained byte rather than returning a permanent gap.
func (s *Store) ReadAppLogFrom(runID string, offset int64) ([]byte, int64, error) {
	data, _, end, err := s.ReadAppLogWindow(runID, offset)
	return data, end, err
}

// ReadAppLogWindow returns retained bytes after offset plus their actual
// absolute start and end cursors. The start may be greater than offset when
// retention has already trimmed older chunks.
func (s *Store) ReadAppLogWindow(runID string, offset int64) ([]byte, int64, int64, error) {
	defer s.timed("ReadAppLogFrom")()
	rows, err := s.db.Query(`
		SELECT start_offset, end_offset, data
		FROM app_log_chunks
		WHERE run_id = ? AND end_offset > ?
		ORDER BY chunk_seq`, runID, offset)
	if err != nil {
		return nil, offset, offset, fmt.Errorf("follow app log: %w", err)
	}
	defer rows.Close()
	var out bytes.Buffer
	startCursor := offset
	end := offset
	found := false
	for rows.Next() {
		var start, chunkEnd int64
		var chunk []byte
		if err := rows.Scan(&start, &chunkEnd, &chunk); err != nil {
			return nil, offset, offset, fmt.Errorf("scan followed app log chunk: %w", err)
		}
		from := int64(0)
		if offset > start {
			from = offset - start
			if from > int64(len(chunk)) {
				from = int64(len(chunk))
			}
		}
		if !found {
			startCursor = start + from
			found = true
		}
		_, _ = out.Write(chunk[from:])
		if chunkEnd > end {
			end = chunkEnd
		}
	}
	if err := rows.Err(); err != nil {
		return nil, offset, offset, err
	}
	return out.Bytes(), startCursor, end, nil
}

// AppLogEndOffset returns the current monotonic end cursor and whether the run
// has emitted any shared output.
func (s *Store) AppLogEndOffset(runID string) (int64, bool, error) {
	defer s.timed("AppLogEndOffset")()
	var end sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(end_offset) FROM app_log_chunks WHERE run_id = ?`, runID).Scan(&end); err != nil {
		return 0, false, fmt.Errorf("app log end offset: %w", err)
	}
	return end.Int64, end.Valid, nil
}

// AppLogStatsForRuns fetches source-list metadata in one query.
func (s *Store) AppLogStatsForRuns(runIDs []string) (map[string]AppLogStats, error) {
	defer s.timed("AppLogStatsForRuns")()
	out := make(map[string]AppLogStats, len(runIDs))
	if len(runIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(runIDs))
	for i := range runIDs {
		args[i] = runIDs[i]
	}
	query := `SELECT run_id, SUM(end_offset - start_offset), MAX(created_at)
		FROM app_log_chunks WHERE run_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(runIDs)), ",") + `)
		GROUP BY run_id`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("app log stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var runID string
		var size, updated int64
		if err := rows.Scan(&runID, &size, &updated); err != nil {
			return nil, fmt.Errorf("scan app log stats: %w", err)
		}
		out[runID] = AppLogStats{SizeBytes: size, UpdatedAt: time.UnixMilli(updated)}
	}
	return out, rows.Err()
}

// AppLogWriter batches concurrent stdout/stderr writes into durable chunks.
// Failed flushes retain the bounded buffer for a later retry; Write still
// succeeds so a transient database outage cannot terminate application output.
type AppLogWriter struct {
	appendChunk func(runID string, seq, startOffset int64, data []byte, maxBytes int64, createdAt time.Time) error
	metrics     AppLogMetrics
	runID       string
	maxBytes    int64

	mu           sync.Mutex
	pending      []appLogPendingChunk
	pendingBytes int64
	nextSeq      int64
	nextOffset   int64
	closed       bool
	lastError    error
	stop         chan struct{}
	done         chan struct{}
}

type appLogPendingChunk struct {
	seq      int64
	start    int64
	queuedAt time.Time
	data     []byte
}

func (s *Store) NewAppLogWriter(runID string, maxBytes int64) (*AppLogWriter, error) {
	if runID == "" {
		return nil, errors.New("app log writer: run ID is required")
	}
	if maxBytes <= 0 {
		maxBytes = AppLogRetentionBytes
	}
	w := &AppLogWriter{
		appendChunk: s.AppendAppLogChunk,
		metrics:     s.appLogMetricsRecorder(),
		runID:       runID,
		maxBytes:    maxBytes,
		stop:        make(chan struct{}), done: make(chan struct{}),
	}
	go w.flushLoop()
	return w, nil
}

func (w *AppLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	written := len(p)
	for len(p) > 0 {
		if n := len(w.pending); n > 0 && len(w.pending[n-1].data) < appLogChunkBytes {
			room := appLogChunkBytes - len(w.pending[n-1].data)
			if room > len(p) {
				room = len(p)
			}
			w.pending[n-1].data = append(w.pending[n-1].data, p[:room]...)
			w.pendingBytes += int64(room)
			w.addPendingBytes(int64(room))
			w.nextOffset += int64(room)
			p = p[room:]
			continue
		}
		take := len(p)
		if take > appLogChunkBytes {
			take = appLogChunkBytes
		}
		chunk := appLogPendingChunk{
			seq: w.nextSeq, start: w.nextOffset,
			queuedAt: time.Now(),
			data:     append([]byte(nil), p[:take]...),
		}
		w.pending = append(w.pending, chunk)
		w.pendingBytes += int64(take)
		w.addPendingBytes(int64(take))
		w.nextSeq++
		w.nextOffset += int64(take)
		p = p[take:]
	}
	w.trimPendingLocked()
	// Persistence errors are retried and reported by Close. The runtime must
	// never interpret a shared-log outage as a stdout/stderr failure.
	return written, nil
}

func (w *AppLogWriter) trimPendingLocked() {
	for w.pendingBytes > w.maxBytes && len(w.pending) > 0 {
		drop := w.pendingBytes - w.maxBytes
		if int64(len(w.pending[0].data)) <= drop {
			drop = int64(len(w.pending[0].data))
			w.pendingBytes -= drop
			w.pending = w.pending[1:]
			w.dropPendingBytes(drop)
			continue
		}
		w.pending[0].data = append([]byte(nil), w.pending[0].data[drop:]...)
		w.pending[0].start += drop
		w.pendingBytes -= drop
		w.dropPendingBytes(drop)
	}
}

func (w *AppLogWriter) addPendingBytes(delta int64) {
	if w.metrics != nil && delta != 0 {
		w.metrics.AddAppLogPendingBytes(delta)
	}
}

func (w *AppLogWriter) recordDroppedBytes(bytes int64) {
	if bytes <= 0 {
		return
	}
	if w.metrics != nil {
		w.metrics.RecordAppLogDroppedBytes(bytes)
	}
}

func (w *AppLogWriter) dropPendingBytes(bytes int64) {
	w.addPendingBytes(-bytes)
	w.recordDroppedBytes(bytes)
}

func (w *AppLogWriter) flushLoop() {
	ticker := time.NewTicker(appLogFlushInterval)
	defer ticker.Stop()
	defer close(w.done)
	for {
		select {
		case <-ticker.C:
			for {
				had, err := w.flushOne()
				if !had || err != nil {
					break
				}
			}
		case <-w.stop:
			return
		}
	}
}

func (w *AppLogWriter) flushOne() (bool, error) {
	w.mu.Lock()
	if len(w.pending) == 0 {
		w.mu.Unlock()
		return false, nil
	}
	chunk := w.pending[0]
	w.pending = w.pending[1:]
	w.pendingBytes -= int64(len(chunk.data))
	w.addPendingBytes(-int64(len(chunk.data)))
	w.mu.Unlock()

	started := time.Now()
	err := w.appendChunk(w.runID, chunk.seq, chunk.start, chunk.data, w.maxBytes, started.UTC())
	if w.metrics != nil {
		result := "ok"
		lag := time.Duration(0)
		if err != nil {
			result = "error"
		} else {
			lag = time.Since(chunk.queuedAt)
		}
		w.metrics.RecordAppLogFlush(result, time.Since(started), lag)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err == nil {
		w.lastError = nil
		return true, nil
	}
	w.lastError = err
	// Requeue only the still-retainable suffix. New writes may have advanced the
	// retention window while this database call was in flight.
	cutoff := w.nextOffset - w.maxBytes
	end := chunk.start + int64(len(chunk.data))
	if end > cutoff {
		if chunk.start < cutoff {
			drop := cutoff - chunk.start
			chunk.data = append([]byte(nil), chunk.data[drop:]...)
			chunk.start = cutoff
			w.recordDroppedBytes(drop)
		}
		w.pending = append([]appLogPendingChunk{chunk}, w.pending...)
		w.pendingBytes += int64(len(chunk.data))
		w.addPendingBytes(int64(len(chunk.data)))
		w.trimPendingLocked()
	} else {
		w.recordDroppedBytes(int64(len(chunk.data)))
	}
	return true, err
}

func (w *AppLogWriter) Close() error {
	w.mu.Lock()
	if w.closed {
		err := w.lastError
		w.mu.Unlock()
		return err
	}
	w.closed = true
	w.mu.Unlock()
	close(w.stop)
	<-w.done
	for {
		had, err := w.flushOne()
		if err != nil {
			// Close has stopped the retry loop. Any bytes still queued after a
			// failed final flush can no longer be persisted, so remove them from
			// the live backlog gauge and account for the loss explicitly.
			w.mu.Lock()
			dropped := w.pendingBytes
			w.pending = nil
			w.pendingBytes = 0
			w.dropPendingBytes(dropped)
			w.mu.Unlock()
			return err
		}
		if !had {
			return err
		}
	}
}

// AppLogReader exposes shared output with the same Tail/Follow contract as the
// process package's local-file reader.
type AppLogReader struct {
	store *Store
	runID string
}

func (s *Store) NewAppLogReader(runID string) *AppLogReader {
	return &AppLogReader{store: s, runID: runID}
}

// ReadAll returns the byte-exact retained snapshot for this immutable run.
func (r *AppLogReader) ReadAll() ([]byte, error) {
	return r.store.ReadAppLog(r.runID)
}

// SnapshotTail captures retained lines and their end cursor from one database
// read, closing the gap between an initial tail and live following.
func (r *AppLogReader) SnapshotTail(n int) ([]logstream.Record, int64, error) {
	data, start, end, err := r.store.ReadAppLogWindow(r.runID, 0)
	if err != nil {
		return nil, 0, err
	}
	records := logstream.RecordsFromBytes(data, start)
	if n >= 0 && len(records) > n {
		records = records[len(records)-n:]
	}
	return records, end, nil
}

func (r *AppLogReader) Tail(n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	data, err := r.store.ReadAppLog(r.runID)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

func (r *AppLogReader) Follow(ctx context.Context, lines chan<- string) {
	offset, _, err := r.store.AppLogEndOffset(r.runID)
	if err != nil {
		offset = 0
	}
	records := make(chan logstream.Record, cap(lines))
	go r.FollowFrom(ctx, offset, records)
	for {
		select {
		case record := <-records:
			select {
			case lines <- record.Line:
			case <-ctx.Done():
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// FollowFrom streams records after an absolute shared-log cursor.
func (r *AppLogReader) FollowFrom(ctx context.Context, offset int64, records chan<- logstream.Record) {
	ticker := time.NewTicker(appLogFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		data, start, end, err := r.store.ReadAppLogWindow(r.runID, offset)
		if err != nil || len(data) == 0 {
			continue
		}
		gapBeforeNext := start > offset
		for _, record := range logstream.RecordsFromBytes(data, start) {
			if gapBeforeNext {
				record.GapBefore = true
				gapBeforeNext = false
			}
			select {
			case records <- record:
			case <-ctx.Done():
				return
			}
		}
		offset = end
	}
}

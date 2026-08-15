package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/logstream"
)

// fanoutLogWriter makes the local capped file the primary runtime sink and
// mirrors successful bytes to shared storage. Shared failures are reported but
// never propagated through Write: stdout persistence must not affect process
// health, and the shared writer owns bounded retry buffering.
type fanoutLogWriter struct {
	local  io.WriteCloser
	shared io.WriteCloser
	run    LogRun
}

func (w *fanoutLogWriter) Write(p []byte) (int, error) {
	n, err := w.local.Write(p)
	if n > 0 {
		if _, sharedErr := w.shared.Write(p[:n]); sharedErr != nil {
			slog.Error("manager: mirror app log", "slug", w.run.Slug, "idx", w.run.ReplicaIndex, "run_id", w.run.RunID, "err", sharedErr)
		}
	}
	return n, err
}

func (w *fanoutLogWriter) Close() error {
	localErr := w.local.Close()
	sharedErr := w.shared.Close()
	if sharedErr != nil {
		slog.Error("manager: flush shared app log", "slug", w.run.Slug, "idx", w.run.ReplicaIndex, "run_id", w.run.RunID, "err", sharedErr)
	}
	return errors.Join(localErr, sharedErr)
}

// DefaultLogMaxSize is the per-app log file size cap (5 MB). When exceeded,
// the file is rotated to app.log.1 and a fresh file is started.
const DefaultLogMaxSize = 5 << 20

// LogFile is a size-capped, append-only log destination for one app process.
// It implements io.WriteCloser and is safe for concurrent writes from the
// stdout and stderr goroutines that the OS spawns when cmd.Stdout and
// cmd.Stderr are both set to the same writer.
type LogFile struct {
	mu      sync.Mutex
	file    *os.File
	path    string
	backup  string
	size    int64
	maxSize int64
}

// OpenLogFile opens or creates the log file at path for appending.
func OpenLogFile(path string, maxSize int64) (*LogFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &LogFile{
		file:    f,
		path:    path,
		backup:  path + ".1",
		size:    info.Size(),
		maxSize: maxSize,
	}, nil
}

// Write implements io.Writer. Rotates when the size cap would be exceeded.
func (l *LogFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size+int64(len(p)) > l.maxSize {
		l.rotate()
	}
	n, err := l.file.Write(p)
	l.size += int64(n)
	return n, err
}

// rotate renames the current file to <path>.1 and opens a fresh file.
// Must be called with l.mu held.
func (l *LogFile) rotate() {
	l.file.Close()
	if err := os.Rename(l.path, l.backup); err != nil {
		// Rename failed — reopen the existing file for appending so writes continue.
		if f, err2 := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640); err2 == nil {
			l.file = f
		}
		return
	}
	// Rename succeeded — open a fresh file at the primary path.
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		// Can't open new file — fall back to the backup so writes don't stop.
		if f2, err2 := os.OpenFile(l.backup, os.O_APPEND|os.O_WRONLY, 0o640); err2 == nil {
			l.file = f2
		}
		return
	}
	l.file = f
	l.size = 0
}

// Close flushes and closes the underlying file.
func (l *LogFile) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}

// LogReader reads from an app log file on disk. Its Tail and Follow methods
// open independent read handles so they work regardless of whether the write
// side (LogFile) is open or closed.
type LogReader struct {
	path string
}

// LogSource describes one retained replica log on local storage. Replica log
// files outlive their process-manager entries (for example after scale-down),
// so discovery deliberately comes from disk rather than the live process pool.
type LogSource struct {
	RunID      string
	Index      int
	SizeBytes  int64
	ModifiedAt time.Time
}

const logRunsDir = "logs"

func logRunFilename(index int, runID string) string {
	return "replica-" + strconv.Itoa(index) + "-" + runID + ".log"
}

func logRunPath(appsDir, slug string, index int, runID string) string {
	return filepath.Join(appsDir, slug, logRunsDir, logRunFilename(index, runID))
}

func parseLogRunFile(name string) (index int, runID string, backup bool, ok bool) {
	switch {
	case strings.HasSuffix(name, ".log.1"):
		backup = true
		name = strings.TrimSuffix(name, ".log.1")
	case strings.HasSuffix(name, ".log"):
		name = strings.TrimSuffix(name, ".log")
	default:
		return 0, "", false, false
	}
	if !strings.HasPrefix(name, "replica-") {
		return 0, "", false, false
	}
	body := strings.TrimPrefix(name, "replica-")
	cut := strings.IndexByte(body, '-')
	if cut <= 0 {
		return 0, "", false, false
	}
	index, err := strconv.Atoi(body[:cut])
	runID = body[cut+1:]
	if err != nil || index < 0 || index > 255 || !validLogRunID(runID) {
		return 0, "", false, false
	}
	return index, runID, backup, true
}

// ListLogRuns returns immutable run files newest-first. The UUID is kept in the
// filename so history remains readable even while the database is unavailable,
// and the replica index allows latest-run compatibility lookup after restart.
func ListLogRuns(appsDir, slug string) ([]LogSource, error) {
	dir := filepath.Join(appsDir, slug, logRunsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []LogSource{}, nil
		}
		return nil, err
	}
	sources := make([]LogSource, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		index, runID, backup, ok := parseLogRunFile(name)
		if entry.IsDir() || !ok || backup {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		sources = append(sources, LogSource{
			RunID: runID, Index: index, SizeBytes: info.Size(), ModifiedAt: info.ModTime(),
		})
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].ModifiedAt.Equal(sources[j].ModifiedAt) {
			return sources[i].RunID > sources[j].RunID
		}
		return sources[i].ModifiedAt.After(sources[j].ModifiedAt)
	})
	return sources, nil
}

// PruneLogRunFiles removes immutable primary and rotated files whose run IDs no
// longer exist in the database. It ignores legacy app-N.log files, malformed
// names, symlinks, and non-regular files.
func PruneLogRunFiles(appsDir string, retained map[string]struct{}) (int, error) {
	apps, err := os.ReadDir(appsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	var errs []error
	for _, app := range apps {
		if !app.IsDir() {
			continue
		}
		dir := filepath.Join(appsDir, app.Name(), logRunsDir)
		files, err := os.ReadDir(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("read %s: %w", dir, err))
			}
			continue
		}
		for _, file := range files {
			_, runID, _, ok := parseLogRunFile(file.Name())
			if !ok || file.IsDir() {
				continue
			}
			if _, keep := retained[runID]; keep {
				continue
			}
			info, err := file.Info()
			if err != nil || !info.Mode().IsRegular() {
				if err != nil {
					errs = append(errs, fmt.Errorf("stat %s: %w", filepath.Join(dir, file.Name()), err))
				}
				continue
			}
			path := filepath.Join(dir, file.Name())
			if err := os.Remove(path); err != nil {
				errs = append(errs, fmt.Errorf("remove %s: %w", path, err))
				continue
			}
			removed++
		}
	}
	return removed, errors.Join(errs...)
}

func validLogRunID(runID string) bool {
	if len(runID) != 36 {
		return false
	}
	for i, r := range runID {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// ListLogSources returns the primary replica log files retained for an app,
// ordered by replica index. Rotated .log.1 backups are an implementation detail
// of the primary stream and are not returned as separate replicas.
func ListLogSources(appsDir, slug string) ([]LogSource, error) {
	dir := filepath.Join(appsDir, slug)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []LogSource{}, nil
		}
		return nil, err
	}

	sources := make([]LogSource, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "app-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		raw := strings.TrimSuffix(strings.TrimPrefix(name, "app-"), ".log")
		index, err := strconv.Atoi(raw)
		if err != nil || index < 0 || index > 255 {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		sources = append(sources, LogSource{
			Index: index, SizeBytes: info.Size(), ModifiedAt: info.ModTime(),
		})
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Index < sources[j].Index })
	return sources, nil
}

// NewLogReader creates a reader for the log file at path.
func NewLogReader(path string) *LogReader {
	return &LogReader{path: path}
}

// ReadAll returns the byte-exact contents of the currently retained primary
// log. LogFile bounds this file to DefaultLogMaxSize; callers that need a
// human-oriented tail should continue to use Tail so they avoid reading it all.
func (r *LogReader) ReadAll() ([]byte, error) {
	return os.ReadFile(r.path)
}

// SnapshotTail returns a tail and the exact file cursor captured with it. A
// follower starting from that cursor cannot miss output written after this
// snapshot, unlike a separate Tail followed by a fresh end-of-file lookup.
func (r *LogReader) SnapshotTail(n int) ([]logstream.Record, int64, error) {
	f, err := os.Open(r.path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	end := info.Size()
	if n <= 0 || end == 0 {
		return nil, end, nil
	}

	const chunkSize = 32 * 1024
	var data []byte
	pos := end
	newlines := 0
	for pos > 0 && newlines <= n {
		read := int64(chunkSize)
		if pos < read {
			read = pos
		}
		pos -= read
		chunk := make([]byte, read)
		if _, err := f.ReadAt(chunk, pos); err != nil && err != io.EOF {
			return nil, 0, err
		}
		data = append(chunk, data...)
		newlines = bytes.Count(data, []byte{'\n'})
	}
	records := logstream.RecordsFromBytes(data, pos)
	if len(records) > n {
		records = records[len(records)-n:]
	}
	return records, end, nil
}

// Tail returns the last n lines from the log file in chronological order. It
// reads backward from the end in chunks, so the work is proportional to the
// size of the returned tail rather than the whole (up to multi-MB) file - the
// hot path for the log viewer and every new SSE follow connection.
func (r *LogReader) Tail(n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	records, _, err := r.SnapshotTail(n)
	if err != nil {
		return nil, err
	}
	lines := make([]string, len(records))
	for i, record := range records {
		lines[i] = record.Line
	}
	return lines, nil
}

// maxLogFollowRead bounds one poll while still accommodating the capped primary
// file plus one unusually long line written across the snapshot boundary.
const maxLogFollowRead = DefaultLogMaxSize + 512*1024

// Follow sends new lines written to the log file to lines until ctx is
// cancelled. It polls the file at 100 ms intervals.
func (r *LogReader) Follow(ctx context.Context, lines chan<- string) {
	var offset int64
	if info, err := os.Stat(r.path); err == nil {
		offset = info.Size()
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

// FollowFrom sends records written strictly after offset. If rotation replaced
// the primary with a shorter file, the cursor resets to the new file's start so
// the follower continues instead of seeking forever beyond EOF.
func (r *LogReader) FollowFrom(ctx context.Context, offset int64, records chan<- logstream.Record) {
	gapBeforeNext := false

	// A single Ticker reused across iterations avoids the per-iteration timer
	// allocation (and goroutine) that time.After would leak over a long-lived
	// follow session.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		f, err := os.Open(r.path)
		if err != nil {
			continue
		}
		if info, statErr := f.Stat(); statErr == nil && info.Size() < offset {
			offset = 0
			gapBeforeNext = true
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(f, maxLogFollowRead))
		f.Close()
		if readErr != nil || len(data) == 0 {
			continue
		}
		for _, record := range logstream.RecordsFromBytes(data, offset) {
			if gapBeforeNext {
				record.GapBefore = true
				gapBeforeNext = false
			}
			select {
			case records <- record:
				offset = record.EndOffset
			case <-ctx.Done():
				return
			}
		}
	}
}

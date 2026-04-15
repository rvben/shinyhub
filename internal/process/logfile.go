package process

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

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
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
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
		if f, err2 := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err2 == nil {
			l.file = f
		}
		return
	}
	// Rename succeeded — open a fresh file at the primary path.
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		// Can't open new file — fall back to the backup so writes don't stop.
		if f2, err2 := os.OpenFile(l.backup, os.O_APPEND|os.O_WRONLY, 0644); err2 == nil {
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

// NewLogReader creates a reader for the log file at path.
func NewLogReader(path string) *LogReader {
	return &LogReader{path: path}
}

// Tail returns the last n lines from the log file in chronological order.
func (r *LogReader) Tail(n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	f, err := os.Open(r.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	ring := make([]string, 0, n)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if len(ring) == n {
			ring = ring[1:]
		}
		ring = append(ring, scanner.Text())
	}
	return ring, scanner.Err()
}

// Follow sends new lines written to the log file to lines until ctx is
// cancelled. It polls the file at 100 ms intervals.
func (r *LogReader) Follow(ctx context.Context, lines chan<- string) {
	var offset int64
	if info, err := os.Stat(r.path); err == nil {
		offset = info.Size()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}

		f, err := os.Open(r.path)
		if err != nil {
			continue
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			continue
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				f.Close()
				return
			}
		}
		offset, _ = f.Seek(0, io.SeekCurrent)
		f.Close()
	}
}

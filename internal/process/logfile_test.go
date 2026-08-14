package process

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFile writes raw bytes to a fresh log path and returns a reader for it.
func tailReaderWith(t *testing.T, content string) *LogReader {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return NewLogReader(path)
}

func eqLines(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d lines %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestListLogSources_ReturnsPrimaryReplicaLogsInIndexOrder(t *testing.T) {
	appsDir := t.TempDir()
	dir := filepath.Join(appsDir, "demo")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"app-2.log":        "two\n",
		"app-0.log":        "zero\n",
		"app-2.log.1":      "rotated\n",
		"app-nope.log":     "ignored\n",
		"deploy-hooks.log": "ignored\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}

	got, err := ListLogSources(appsDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sources: %+v", len(got), got)
	}
	if got[0].Index != 0 || got[1].Index != 2 {
		t.Fatalf("source order = %+v, want indices 0,2", got)
	}
	if got[0].SizeBytes != int64(len("zero\n")) || got[1].SizeBytes != int64(len("two\n")) {
		t.Fatalf("source sizes = %+v", got)
	}
}

func TestListLogSources_MissingAppDirectoryIsEmpty(t *testing.T) {
	got, err := ListLogSources(t.TempDir(), "missing")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("got %#v, want an empty non-nil slice", got)
	}
}

func TestPruneLogRunFilesRemovesOnlyUnretainedImmutableFiles(t *testing.T) {
	appsDir := t.TempDir()
	dir := filepath.Join(appsDir, "demo", logRunsDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	keepID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	pruneID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	for _, name := range []string{
		logRunFilename(0, keepID), logRunFilename(0, keepID) + ".1",
		logRunFilename(1, pruneID), logRunFilename(1, pruneID) + ".1",
		"replica-1-not-a-run.log", "notes.txt",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("log\n"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	legacy := filepath.Join(appsDir, "demo", "app-1.log")
	if err := os.WriteFile(legacy, []byte("legacy\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(appsDir, "outside.log")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, logRunFilename(2, pruneID))
	if err := os.Symlink(outside, symlink); err != nil {
		t.Fatal(err)
	}

	removed, err := PruneLogRunFiles(appsDir, map[string]struct{}{keepID: {}})
	if err != nil || removed != 2 {
		t.Fatalf("PruneLogRunFiles = %d, %v, want 2", removed, err)
	}
	for _, path := range []string{
		filepath.Join(dir, logRunFilename(0, keepID)),
		filepath.Join(dir, logRunFilename(0, keepID)+".1"),
		filepath.Join(dir, "replica-1-not-a-run.log"),
		filepath.Join(dir, "notes.txt"), legacy, outside, symlink,
	} {
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("preserved path %s: %v", path, err)
		}
	}
	for _, path := range []string{
		filepath.Join(dir, logRunFilename(1, pruneID)),
		filepath.Join(dir, logRunFilename(1, pruneID)+".1"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("pruned path still exists: %s", path)
		}
	}
}

// TestTail_EdgeCases pins the exact line semantics Tail must preserve: last-n in
// order, files with and without a trailing newline, CRLF stripping, n larger
// than the line count, and n<=0. These guard the backward-read implementation.
func TestTail_EdgeCases(t *testing.T) {
	r := tailReaderWith(t, "a\nb\nc\n")
	if got, _ := r.Tail(2); true {
		eqLines(t, got, []string{"b", "c"})
	}
	if got, _ := r.Tail(10); true {
		eqLines(t, got, []string{"a", "b", "c"})
	}

	// No trailing newline: the final line must still be returned.
	r = tailReaderWith(t, "a\nb\nc")
	if got, _ := r.Tail(2); true {
		eqLines(t, got, []string{"b", "c"})
	}

	// CRLF endings: the trailing \r is stripped, matching bufio.Scanner.
	r = tailReaderWith(t, "a\r\nb\r\nc\r\n")
	if got, _ := r.Tail(2); true {
		eqLines(t, got, []string{"b", "c"})
	}

	// n <= 0 and empty file return nothing.
	if got, _ := tailReaderWith(t, "x\ny\n").Tail(0); got != nil {
		t.Errorf("Tail(0) = %v, want nil", got)
	}
	if got, _ := tailReaderWith(t, "").Tail(5); len(got) != 0 {
		t.Errorf("Tail on empty file = %v, want none", got)
	}
}

// TestTail_LargeFileMultiChunk forces the backward reader across many chunk
// boundaries (file far larger than the read chunk) and asserts the last n lines
// are exact - the boundary assembly is where a naive reverse reader breaks.
func TestTail_LargeFileMultiChunk(t *testing.T) {
	var sb strings.Builder
	const total = 5000
	for i := 0; i < total; i++ {
		fmt.Fprintf(&sb, "line-%05d\n", i)
	}
	r := tailReaderWith(t, sb.String())
	got, err := r.Tail(3)
	if err != nil {
		t.Fatal(err)
	}
	eqLines(t, got, []string{"line-04997", "line-04998", "line-04999"})
}

func TestLogFile_WriteAndTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	lf, err := OpenLogFile(path, 1<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer lf.Close()

	lf.Write([]byte("line one\nline two\nline three\n"))

	lr := NewLogReader(path)
	lines, err := lr.Tail(10)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "line one" || lines[2] != "line three" {
		t.Errorf("unexpected lines: %v", lines)
	}
}

func TestLogFile_TailLimitsLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	lf, err := OpenLogFile(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()

	for i := 0; i < 10; i++ {
		lf.Write([]byte("line\n"))
	}

	lr := NewLogReader(path)
	lines, err := lr.Tail(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
}

func TestLogFile_Rotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	// maxSize of 20 bytes to force rotation quickly
	lf, err := OpenLogFile(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()

	lf.Write([]byte(strings.Repeat("x", 25) + "\n"))
	lf.Write([]byte("after rotation\n"))

	// Backup file must exist after rotation
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("expected backup file to exist: %v", err)
	}

	lr := NewLogReader(path)
	lines, _ := lr.Tail(10)
	if len(lines) == 0 || lines[len(lines)-1] != "after rotation" {
		t.Errorf("expected 'after rotation' as last line, got %v", lines)
	}
}

func TestLogReader_Follow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	// Write initial content before Follow starts
	lf, err := OpenLogFile(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	lf.Write([]byte("existing\n"))

	lr := NewLogReader(path)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := make(chan string, 10)
	go lr.Follow(ctx, ch)

	// Write a new line after Follow has started
	time.Sleep(200 * time.Millisecond)
	lf.Write([]byte("new line\n"))

	select {
	case line := <-ch:
		if line != "new line" {
			t.Errorf("expected 'new line', got %q", line)
		}
	case <-ctx.Done():
		t.Error("timed out waiting for Follow to deliver line")
	}
	lf.Close()
}

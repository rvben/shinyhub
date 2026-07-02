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

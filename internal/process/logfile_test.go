package process

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

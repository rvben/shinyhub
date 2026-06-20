package localrun

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchAndRestart_FiresOnChange(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "app.py")
	os.WriteFile(f, []byte("v=1\n"), 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fired := make(chan struct{}, 1)
	go watchAndRestart(ctx, dir, []string{".shinyhub-run", ".venv", ".git"}, func() { fired <- struct{}{} })
	time.Sleep(300 * time.Millisecond)
	os.WriteFile(f, []byte("v=2\n"), 0o644)
	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Fatal("watcher did not fire on file change")
	}
}

func TestWatchAndRestart_NoFireOnExcluded(t *testing.T) {
	dir := t.TempDir()
	// Create a file in an excluded dir.
	excludedDir := filepath.Join(dir, ".venv")
	if err := os.MkdirAll(excludedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	excluded := filepath.Join(excludedDir, "lib.py")
	os.WriteFile(excluded, []byte("v=1\n"), 0o644)

	// Also create a non-excluded file so the watcher has something to scan.
	base := filepath.Join(dir, "app.py")
	os.WriteFile(base, []byte("v=1\n"), 0o644)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fired := make(chan struct{}, 1)
	go watchAndRestart(ctx, dir, []string{".shinyhub-run", ".venv", ".git", "__pycache__", "node_modules"}, func() { fired <- struct{}{} })

	// Let the watcher do an initial scan.
	time.Sleep(300 * time.Millisecond)

	// Modify only the excluded file.
	os.WriteFile(excluded, []byte("v=2\n"), 0o644)

	// The watcher must NOT fire.
	select {
	case <-fired:
		t.Fatal("watcher must not fire for changes inside excluded dirs")
	case <-time.After(1500 * time.Millisecond):
		// Expected: no fire.
	}
}

func TestWatchAndRestart_Debounce(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "app.py")
	os.WriteFile(f, []byte("v=1\n"), 0o644)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	count := 0
	fired := make(chan struct{}, 10)
	go watchAndRestart(ctx, dir, nil, func() {
		count++
		fired <- struct{}{}
	})

	// Initial scan settles.
	time.Sleep(300 * time.Millisecond)

	// Burst of rapid writes.
	for i := 0; i < 5; i++ {
		os.WriteFile(f, []byte("v=burst\n"), 0o644)
	}

	// Wait for debounce to fire at most once (allow up to 2 seconds).
	time.Sleep(1500 * time.Millisecond)

	if count == 0 {
		t.Fatal("watcher must fire at least once after burst")
	}
	if count > 2 {
		t.Fatalf("watcher fired %d times for a burst; expected debounce to <=2", count)
	}
}

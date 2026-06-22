// internal/worker/agent/bundlecache_test.go
package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rvben/shinyhub/internal/bundle"
)

// makeZip builds an in-memory zip with the given files and returns its bytes and
// the content digest the control plane would record for it.
func makeZip(t *testing.T, files map[string]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %q: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip new reader: %v", err)
	}
	digest, err := bundle.DigestZipReader(zr)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	return buf.Bytes(), digest
}

func TestBundleCacheVerifiesAndExtracts(t *testing.T) {
	zipBytes, digest := makeZip(t, map[string]string{"app.py": "print('hi')"})
	fetches := 0
	fetch := func(ctx context.Context, d string) (io.ReadCloser, error) {
		fetches++
		return io.NopCloser(bytes.NewReader(zipBytes)), nil
	}
	cache := NewBundleCache(t.TempDir(), fetch)

	dir, err := cache.Ensure(context.Background(), digest)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "app.py")); string(b) != "print('hi')" {
		t.Fatalf("extracted content wrong: %q", b)
	}

	// Second Ensure is a cache hit: no extra fetch.
	if _, err := cache.Ensure(context.Background(), digest); err != nil {
		t.Fatalf("ensure 2: %v", err)
	}
	if fetches != 1 {
		t.Fatalf("fetches = %d, want 1 (second call must hit cache)", fetches)
	}
}

func TestBundleCacheRejectsDigestMismatch(t *testing.T) {
	zipBytes, _ := makeZip(t, map[string]string{"app.py": "x"})
	fetch := func(ctx context.Context, d string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(zipBytes)), nil
	}
	cache := NewBundleCache(t.TempDir(), fetch)
	_, err := cache.Ensure(context.Background(), "sha256:not-the-real-digest")
	if err == nil {
		t.Fatal("expected digest-mismatch error, got nil")
	}
}

func TestBundleCacheRejectsUnsafeDigest(t *testing.T) {
	cacheRoot := t.TempDir()
	parentDir := filepath.Dir(cacheRoot)
	fetch := func(ctx context.Context, d string) (io.ReadCloser, error) {
		t.Error("fetch should never be called for an unsafe digest")
		return nil, fmt.Errorf("fetch must not be called for unsafe digest")
	}
	cache := NewBundleCache(cacheRoot, fetch)

	// "../escape" has no colon, so ReplaceAll leaves it unchanged; filepath.Join
	// then resolves it to the parent directory -- a genuine traversal.
	_, err := cache.Ensure(context.Background(), "../escape")
	if err == nil {
		t.Fatal("expected path-traversal error, got nil")
	}

	// Nothing should have been created outside the cache root.
	escapedPath := filepath.Join(parentDir, "escape")
	if _, statErr := os.Stat(escapedPath); statErr == nil {
		t.Fatalf("path-traversal: %q was created outside the cache root", escapedPath)
	}
}

func TestBundleCacheDedupsConcurrentPulls(t *testing.T) {
	zipBytes, digest := makeZip(t, map[string]string{"app.py": "concurrent"})

	var fetchCount atomic.Int32
	// Use a gate to widen the concurrency window: all goroutines enter fetch
	// before any of them return, maximising the chance of real concurrent access.
	gate := make(chan struct{})
	fetch := func(ctx context.Context, d string) (io.ReadCloser, error) {
		fetchCount.Add(1)
		<-gate
		return io.NopCloser(bytes.NewReader(zipBytes)), nil
	}

	cache := NewBundleCache(t.TempDir(), fetch)

	const N = 20
	dirs := make([]string, N)
	errs := make([]error, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := range N {
		go func(i int) {
			defer wg.Done()
			dirs[i], errs[i] = cache.Ensure(context.Background(), digest)
		}(i)
	}

	// Let all goroutines that managed to enter fetch proceed.
	close(gate)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}
	for i, d := range dirs {
		if d != dirs[0] {
			t.Errorf("goroutine %d: got dir %q, want %q", i, d, dirs[0])
		}
	}
	if n := fetchCount.Load(); n != 1 {
		t.Errorf("fetch called %d times, want 1", n)
	}
}

// TestBundleCacheRechecksCacheUnderLock guards the stat-miss-then-lock TOCTOU:
// when a concurrent puller installs the cache dir after our initial stat miss
// but before we take the coordination lock, Ensure must re-check under the lock
// and return the hit, never starting a redundant second fetch. The hook
// simulates that winner deterministically; on the pre-fix code this fetches
// once (fails), on the fixed code it fetches zero times.
func TestBundleCacheRechecksCacheUnderLock(t *testing.T) {
	zipBytes, digest := makeZip(t, map[string]string{"app.py": "recheck"})

	var fetchCount atomic.Int32
	fetch := func(ctx context.Context, d string) (io.ReadCloser, error) {
		fetchCount.Add(1)
		return io.NopCloser(bytes.NewReader(zipBytes)), nil
	}
	cache := NewBundleCache(t.TempDir(), fetch)

	cacheDir, err := cache.dirFor(digest)
	if err != nil {
		t.Fatalf("dirFor: %v", err)
	}
	// A winning puller renames its extracted tree into cacheDir before deleting
	// its inflight entry, so model that by creating the dir in the race window.
	cache.testHookAfterStatMiss = func() {
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			t.Errorf("seed cache dir: %v", err)
		}
	}

	dir, err := cache.Ensure(context.Background(), digest)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if dir != cacheDir {
		t.Errorf("dir = %q, want %q", dir, cacheDir)
	}
	if n := fetchCount.Load(); n != 0 {
		t.Errorf("fetch called %d times; the under-lock re-check should have turned the race into a hit", n)
	}
}

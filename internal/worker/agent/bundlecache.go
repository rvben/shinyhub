// internal/worker/agent/bundlecache.go
package agent

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rvben/shinyhub/internal/bundle"
	"github.com/rvben/shinyhub/internal/deploy"
)

// FetchFunc streams the bundle zip for a digest from the control plane.
type FetchFunc func(ctx context.Context, digest string) (io.ReadCloser, error)

// inflightPull tracks an in-progress fetch so concurrent Ensure calls for the
// same digest wait for the primary puller and share its result.
type inflightPull struct {
	done chan struct{}
	dir  string
	err  error
}

// BundleCache stores extracted bundles on local disk keyed by content digest.
// A digest's extracted tree lives at <root>/<sanitized-digest>; the directory is
// created by atomic rename so a partial extraction is never seen as a hit.
type BundleCache struct {
	root  string
	fetch FetchFunc

	mu   sync.Mutex
	keys map[string]*inflightPull // in-flight pulls, to dedup concurrent Ensure
}

func NewBundleCache(root string, fetch FetchFunc) *BundleCache {
	return &BundleCache{root: root, fetch: fetch, keys: map[string]*inflightPull{}}
}

// dirFor maps a digest to its cache directory, replacing the ':' that "sha256:"
// digests carry so the path is portable. It rejects digests whose joined path
// would escape the cache root (path-traversal guard).
func (c *BundleCache) dirFor(digest string) (string, error) {
	dir := filepath.Join(c.root, strings.ReplaceAll(digest, ":", "_"))
	if dir != c.root && !strings.HasPrefix(dir, c.root+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid digest %q: escapes cache root", digest)
	}
	return dir, nil
}

// Ensure returns the local extracted bundle dir for digest, pulling + verifying
// + extracting on a miss. Concurrent Ensure calls for the same digest share one pull.
func (c *BundleCache) Ensure(ctx context.Context, digest string) (retDir string, retErr error) {
	cacheDir, err := c.dirFor(digest)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(cacheDir); err == nil {
		slog.Info("bundle: cache hit", "digest", digest)
		return cacheDir, nil
	}

	// Coordinate concurrent pulls for the same digest.
	c.mu.Lock()
	if p, ok := c.keys[digest]; ok {
		c.mu.Unlock()
		select {
		case <-p.done:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		// p.dir and p.err were written by the primary puller before close(p.done),
		// so the channel close provides the happens-before guarantee -- safe to read.
		return p.dir, p.err
	}
	p := &inflightPull{done: make(chan struct{})}
	c.keys[digest] = p
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.keys, digest)
		// Write result fields before closing the channel so waiters observe them
		// only after the happens-before edge from close(p.done).
		p.dir = retDir
		p.err = retErr
		close(p.done)
		c.mu.Unlock()
	}()

	if err := os.MkdirAll(c.root, 0o755); err != nil {
		return "", fmt.Errorf("cache root: %w", err)
	}

	// Stream the zip to a temp file so we can both digest-verify and extract it.
	tmpZip, err := os.CreateTemp(c.root, "pull-*.zip")
	if err != nil {
		return "", fmt.Errorf("temp zip: %w", err)
	}
	tmpZipPath := tmpZip.Name()
	defer os.Remove(tmpZipPath)

	rc, err := c.fetch(ctx, digest)
	if err != nil {
		tmpZip.Close()
		return "", fmt.Errorf("fetch %s: %w", digest, err)
	}
	if _, err := io.Copy(tmpZip, rc); err != nil {
		rc.Close()
		tmpZip.Close()
		return "", fmt.Errorf("write pulled bundle: %w", err)
	}
	rc.Close()
	if err := tmpZip.Close(); err != nil {
		return "", fmt.Errorf("close temp zip: %w", err)
	}

	// Verify the content digest matches before trusting the bundle.
	zr, err := zip.OpenReader(tmpZipPath)
	if err != nil {
		return "", fmt.Errorf("open pulled zip: %w", err)
	}
	got, derr := bundle.DigestZipReader(&zr.Reader)
	zr.Close()
	if derr != nil {
		return "", fmt.Errorf("digest pulled bundle: %w", derr)
	}
	if got != digest {
		return "", fmt.Errorf("bundle digest mismatch: got %s want %s", got, digest)
	}

	// Extract into a temp dir, then atomically rename into the digest-keyed dir.
	tmpDir, err := os.MkdirTemp(c.root, "extract-*")
	if err != nil {
		return "", fmt.Errorf("temp extract dir: %w", err)
	}
	if err := deploy.ExtractBundle(tmpZipPath, tmpDir); err != nil {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("extract bundle: %w", err)
	}
	if err := os.Rename(tmpDir, cacheDir); err != nil {
		// A concurrent winner may have created cacheDir first; that is fine.
		if _, statErr := os.Stat(cacheDir); statErr == nil {
			os.RemoveAll(tmpDir)
			return cacheDir, nil
		}
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("install bundle dir: %w", err)
	}
	slog.Info("bundle: pulled", "digest", digest)
	return cacheDir, nil
}

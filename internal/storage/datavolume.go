package storage

import (
	"fmt"
	"os"
	"path/filepath"
)

// VolumeRef identifies a per-app mutable data volume shared across the app's
// replicas.
type VolumeRef struct {
	Slug string
	Path string // host path for LocalVolume
}

// DataVolume provisions per-app mutable storage usable by every replica of an
// app. Implementations: LocalVolume (default; single host), SharedFSVolume
// (EFS/NFS, Phase 2+). ShinyHub does not arbitrate concurrent writes - apps
// with replicas > 1 writing shared data need a POSIX shared FS or an external
// store.
type DataVolume interface {
	// Provision ensures the app's data volume exists and returns a ref.
	Provision(slug string) (VolumeRef, error)
}

// LocalVolume is the single-node DataVolume: <Root>/<slug> on local disk,
// created idempotently.
type LocalVolume struct {
	Root string
}

func (v LocalVolume) Provision(slug string) (VolumeRef, error) {
	p := filepath.Join(v.Root, slug)
	if err := os.MkdirAll(p, 0o750); err != nil {
		return VolumeRef{}, fmt.Errorf("provision local volume for %s: %w", slug, err)
	}
	return VolumeRef{Slug: slug, Path: p}, nil
}

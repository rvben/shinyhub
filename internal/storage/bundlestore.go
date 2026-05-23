package storage

// BundleRef identifies a published immutable bundle version.
type BundleRef struct {
	Slug    string
	Version string
	Path    string // local filesystem path for LocalStore
}

// Localization tells a runtime how to reach a bundle on a given tier: a
// mountable local path (LocalStore/SharedFS) is provided directly.
type Localization struct {
	LocalPath string
}

// BundleStore publishes immutable, versioned app bundles and resolves them for
// a runtime tier. Implementations: LocalStore (default), SharedFSStore,
// ObjectStore (Phase 2+).
type BundleStore interface {
	// Put publishes the bundle in dir as (slug, version) and returns a ref.
	Put(slug, version, dir string) (BundleRef, error)
	// Resolve returns how the named tier reaches the referenced bundle.
	Resolve(ref BundleRef, tier string) (Localization, error)
}

// LocalStore is the single-node BundleStore: a bundle is already on local disk,
// so Put records the path and Resolve hands it back for a bind-mount.
type LocalStore struct{}

func (LocalStore) Put(slug, version, dir string) (BundleRef, error) {
	return BundleRef{Slug: slug, Version: version, Path: dir}, nil
}

func (LocalStore) Resolve(ref BundleRef, _ string) (Localization, error) {
	return Localization{LocalPath: ref.Path}, nil
}

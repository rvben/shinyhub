// Package bundle defines the single source of truth for which files may
// appear inside a deploy bundle. Both the CLI zipper and the server
// extractor call Inspect with the same Rules so client-side and
// server-side enforcement cannot drift.
package bundle

import (
	"path"
	"strings"
)

// FilterDecision is the per-entry classification produced by Rules.Inspect.
type FilterDecision int

const (
	// FilterAccept: include in the bundle.
	FilterAccept FilterDecision = iota
	// FilterSkipCacheDir: known build/VCS cache; omit silently, not an error.
	FilterSkipCacheDir
	// FilterRejectDataDir: first segment is exactly "data" — reserved by the
	// platform for the persistent data mount. Hard error.
	FilterRejectDataDir
	// FilterRejectDatasetDir: first segment is "datasets" or
	// ".shinyhub-data" — reserved namespaces for data shipped via push.
	FilterRejectDatasetDir
	// FilterRejectExtension: data-format file (parquet, duckdb, sqlite, …).
	FilterRejectExtension
	// FilterRejectFileSize: single file exceeds Rules.MaxFileBytes.
	FilterRejectFileSize
)

// String renders a decision for log/error messages.
func (d FilterDecision) String() string {
	switch d {
	case FilterAccept:
		return "accept"
	case FilterSkipCacheDir:
		return "skip-cache-dir"
	case FilterRejectDataDir:
		return "reject-data-dir"
	case FilterRejectDatasetDir:
		return "reject-dataset-dir"
	case FilterRejectExtension:
		return "reject-extension"
	case FilterRejectFileSize:
		return "reject-file-size"
	default:
		return "unknown"
	}
}

// Rules is the per-bundle policy. Total bundle size is enforced separately
// at the multipart layer because per-entry inspection cannot know it.
type Rules struct {
	MaxFileBytes   int64    `json:"maxFileBytes"`
	CacheDirs      []string `json:"cacheDirs"`
	DataExtensions []string `json:"dataExtensions"`
	// MaxBundleBytes is informational for clients (UI shows it, server
	// re-asserts via Storage.MaxBundleMB at the multipart boundary).
	MaxBundleBytes int64 `json:"maxBundleBytes"`
}

// DefaultRules returns the policy embedded in v1.
func DefaultRules() Rules {
	return Rules{
		MaxFileBytes: 10 * 1024 * 1024,
		CacheDirs: []string{
			".git", ".venv", "__pycache__", "node_modules", ".renv", ".Rproj.user",
		},
		DataExtensions: []string{
			".parquet",
			".duckdb", ".duckdb.wal",
			".sqlite", ".sqlite3", ".db",
			".rds",
			".feather", ".arrow",
			".h5", ".hdf5",
		},
		MaxBundleBytes: 128 * 1024 * 1024,
	}
}

// Inspect classifies a single bundle entry. relPath is forward-slash
// separated, relative to the bundle root, with no leading slash.
func (r Rules) Inspect(relPath string, size int64) FilterDecision {
	clean := path.Clean(strings.TrimPrefix(relPath, "/"))
	if clean == "." || clean == "" {
		return FilterAccept
	}
	first := clean
	if i := strings.IndexByte(clean, '/'); i >= 0 {
		first = clean[:i]
	}

	if first == "data" {
		return FilterRejectDataDir
	}
	if first == "datasets" || first == ".shinyhub-data" {
		return FilterRejectDatasetDir
	}
	for _, c := range r.CacheDirs {
		if first == c {
			return FilterSkipCacheDir
		}
	}
	lower := strings.ToLower(clean)
	for _, ext := range r.DataExtensions {
		if strings.HasSuffix(lower, strings.ToLower(ext)) {
			return FilterRejectExtension
		}
	}
	if r.MaxFileBytes > 0 && size > r.MaxFileBytes {
		return FilterRejectFileSize
	}
	return FilterAccept
}

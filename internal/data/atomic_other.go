//go:build !linux

package data

import "errors"

// errAtomicUnsupported is always returned off Linux: there is no portable
// openat2 equivalent, so callers fall back to the SafeJoin check followed by
// a plain rename/unlink.
var errAtomicUnsupported = errors.New("atomic resolution unsupported on this platform")

func atomicReplace(_, _, _ string) error { return errAtomicUnsupported }

func atomicDelete(_, _ string) error { return errAtomicUnsupported }

package api

import (
	"errors"
	"io/fs"
	"net/http"
	"syscall"
)

// bundleStorageFailure classifies a filesystem failure raised while writing or
// extracting an uploaded bundle into a status and a message the deployer can
// act on, or ok=false when the cause is not one this function can describe
// honestly (the caller then falls back to a generic internal error).
//
// The point is that "internal error" tells a deployer nothing about whose
// problem it is. A full disk, a read-only mount and a permission problem on the
// apps directory are all the operator's to fix and all indistinguishable from a
// broken bundle from the outside, so a deployer without server log access
// re-uploads a perfectly good bundle over and over. Naming the cause routes the
// report to the person who can act.
//
// No host path is ever included: the messages are fixed strings, and the errno
// is read through errors.Is rather than by formatting the error. Errno alone
// leaks nothing about the layout of the server's filesystem.
func bundleStorageFailure(err error) (status int, msg string, ok bool) {
	switch {
	case errors.Is(err, syscall.ENOSPC):
		return http.StatusInsufficientStorage,
			"the server ran out of disk space while storing this bundle. No deployment was made; ask the operator to free space on the ShinyHub apps volume, then deploy again.", true
	case errors.Is(err, syscall.EDQUOT):
		return http.StatusInsufficientStorage,
			"the server's storage quota is exhausted, so this bundle could not be stored. No deployment was made; this is a server-side limit and needs the operator.", true
	case errors.Is(err, syscall.EROFS):
		return http.StatusInternalServerError,
			"the server's apps directory is mounted read-only, so this bundle could not be stored. This is a server configuration problem, not a problem with your bundle.", true
	case errors.Is(err, fs.ErrPermission):
		return http.StatusInternalServerError,
			"the server could not write into its apps directory (permission denied). This is a server-side filesystem problem, not a problem with your bundle.", true
	}
	return 0, "", false
}

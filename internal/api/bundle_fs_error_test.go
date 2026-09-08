package api

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"syscall"
	"testing"
)

// TestBundleStorageFailure covers the errnos a real host raises that a deployer
// cannot provoke from a test (a full disk, an exhausted quota, a read-only
// mount), and pins the boundary: an error this function cannot describe
// honestly must fall through so the caller answers "internal error" rather than
// inventing a cause.
func TestBundleStorageFailure(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantOK     bool
		wantStatus int
		wantPhrase string
	}{
		{
			name: "out of disk space", err: syscall.ENOSPC, wantOK: true,
			wantStatus: http.StatusInsufficientStorage, wantPhrase: "ran out of disk space",
		},
		{
			name: "storage quota exhausted", err: syscall.EDQUOT, wantOK: true,
			wantStatus: http.StatusInsufficientStorage, wantPhrase: "storage quota is exhausted",
		},
		{
			name: "read-only filesystem", err: syscall.EROFS, wantOK: true,
			wantStatus: http.StatusInternalServerError, wantPhrase: "read-only",
		},
		{
			name: "permission denied", err: syscall.EACCES, wantOK: true,
			wantStatus: http.StatusInternalServerError, wantPhrase: "permission denied",
		},
		{
			// The handler wraps with context before this is reached, so the
			// classification must survive wrapping rather than depending on the
			// error being the bare errno.
			name: "wrapped out of disk space", err: fmt.Errorf("write bundle file: %w", syscall.ENOSPC), wantOK: true,
			wantStatus: http.StatusInsufficientStorage, wantPhrase: "ran out of disk space",
		},
		{
			// deploy.ExtractBundle strips the host path but keeps the errno via
			// %w, which is what makes this reachable at all for extraction.
			name: "path error keeping its errno", err: &fs.PathError{Op: "mkdir", Path: "/srv/apps/x", Err: syscall.ENOSPC}, wantOK: true,
			wantStatus: http.StatusInsufficientStorage, wantPhrase: "ran out of disk space",
		},
		{
			name: "a cause with no honest description", err: errors.New("zip: not a valid archive"), wantOK: false,
		},
		{
			// EINTR is a filesystem errno but says nothing an operator can act
			// on, so it must not be dressed up as one of the known causes.
			name: "an unlisted errno", err: syscall.EINTR, wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, msg, ok := bundleStorageFailure(tc.err)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (msg %q)", ok, tc.wantOK, msg)
			}
			if !tc.wantOK {
				if status != 0 || msg != "" {
					t.Errorf("an unclassified cause must return no status and no message, got %d %q", status, msg)
				}
				return
			}
			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d", status, tc.wantStatus)
			}
			if !strings.Contains(msg, tc.wantPhrase) {
				t.Errorf("message should contain %q, got %q", tc.wantPhrase, msg)
			}
			// Every classified message must say the deploy did not happen or
			// whose problem it is, so the deployer knows what to do next.
			if !strings.Contains(msg, "No deployment was made") && !strings.Contains(msg, "not a problem with your bundle") {
				t.Errorf("message must tell the deployer where the fault lies, got %q", msg)
			}
		})
	}
}

// TestBundleStorageFailureNeverEchoesAPath guards the reason this returns fixed
// strings instead of formatting the error: an *fs.PathError carries the
// server's absolute path, and formatting it here would publish the operator's
// filesystem layout to anyone who can deploy.
func TestBundleStorageFailureNeverEchoesAPath(t *testing.T) {
	secret := "/srv/shinyhub/data/apps/customer-one"
	_, msg, ok := bundleStorageFailure(&fs.PathError{Op: "mkdir", Path: secret, Err: syscall.EACCES})
	if !ok {
		t.Fatal("EACCES inside a PathError must classify")
	}
	if strings.Contains(msg, secret) || strings.Contains(msg, "/srv") {
		t.Errorf("message leaked the host path: %q", msg)
	}
}

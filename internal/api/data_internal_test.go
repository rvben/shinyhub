package api

import (
	"errors"
	"net/http"
	"os"
	"syscall"
	"testing"
)

// TestStorageWriteStatus_ClassifiesENOSPC verifies a disk-full write is mapped
// to 507 Insufficient Storage (so a client/operator can distinguish "out of
// space" from a generic failure) while any other write error is a 500. This
// exercises the ENOSPC branch that cannot be triggered against a real disk.
func TestStorageWriteStatus_ClassifiesENOSPC(t *testing.T) {
	enospc := &os.PathError{Op: "write", Path: "/data/app/x", Err: syscall.ENOSPC}
	if status, _ := storageWriteStatus(enospc); status != http.StatusInsufficientStorage {
		t.Errorf("ENOSPC must map to 507, got %d", status)
	}
	if status, _ := storageWriteStatus(errors.New("some other io error")); status != http.StatusInternalServerError {
		t.Errorf("a generic write error must map to 500, got %d", status)
	}
}

package process

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeClock lets rotation-backoff tests control elapsed time directly instead
// of sleeping, so they are deterministic under load rather than timing-based.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// openLogFileForRotationTest opens a LogFile the way OpenLogFile does, without
// requiring the caller to duplicate the perm/flags used for the initial file.
func openLogFileForRotationTest(t *testing.T, path string, maxSize int64) *LogFile {
	t.Helper()
	lf, err := OpenLogFile(path, maxSize)
	if err != nil {
		t.Fatalf("OpenLogFile: %v", err)
	}
	t.Cleanup(func() { lf.Close() })
	return lf
}

// TestLogFile_Rotate_RenameFailure_RetriesWithBackoff asserts the size
// counter's effect on a persistently failing rotation: today (unfixed) every
// oversized write past the cap retries the rotation, so the rename hook is
// invoked once per write; after the fix, a failed rotation backs off and is
// retried at most once regardless of how many further oversized writes occur
// within the backoff window. Every byte must still land either way.
func TestLogFile_Rotate_RenameFailure_RetriesWithBackoff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	lf := openLogFileForRotationTest(t, path, 5)

	clock := &fakeClock{t: time.Unix(0, 0)}
	lf.now = clock.now

	renameCalls := 0
	lf.rename = func(oldpath, newpath string) error {
		renameCalls++
		return errors.New("inject: rename always fails")
	}

	if _, err := lf.Write([]byte("AAAAA")); err != nil {
		t.Fatalf("initial write: %v", err)
	}
	for _, b := range []byte("BCDE") {
		if _, err := lf.Write([]byte{b}); err != nil {
			t.Fatalf("write %q during failed rotation: %v", b, err)
		}
	}

	if renameCalls != 1 {
		t.Fatalf("rename hook called %d times across 4 over-cap writes, want 1 (backoff should suppress the rest)", renameCalls)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "AAAAABCDE" {
		t.Fatalf("primary content = %q, want %q (a byte was lost)", got, "AAAAABCDE")
	}
}

// TestLogFile_Rotate_OversizedFirstWrite_PreservesExistingBackup asserts the
// size counter's effect on a single write that exceeds maxSize while the file
// is still empty: today (unfixed) this unconditionally rotates, which renames
// the empty file over any existing <path>.1, destroying it. After the fix, a
// write against an empty file is never rotated.
func TestLogFile_Rotate_OversizedFirstWrite_PreservesExistingBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	const existingBackup = "prior backup content\n"
	if err := os.WriteFile(path+".1", []byte(existingBackup), 0o640); err != nil {
		t.Fatal(err)
	}

	lf := openLogFileForRotationTest(t, path, 10)
	oversized := make([]byte, 50)
	for i := range oversized {
		oversized[i] = 'X'
	}
	if _, err := lf.Write(oversized); err != nil {
		t.Fatalf("oversized first write: %v", err)
	}

	got, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != existingBackup {
		t.Fatalf(".1 = %q, want untouched %q", got, existingBackup)
	}
}

// TestLogFile_Rotate_PrimaryAndBackupReopenBothFail asserts the size
// counter's effect when a rotation renames the primary away and then cannot
// open a replacement by any path: today (unfixed) the old handle was already
// closed before either open was attempted, so the write that triggered
// rotation lands on a closed file and its bytes are lost. After the fix, the
// old handle is never closed until a replacement is ready, so the same write
// succeeds.
func TestLogFile_Rotate_PrimaryAndBackupReopenBothFail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	lf := openLogFileForRotationTest(t, path, 10)

	lf.openFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		return nil, errors.New("inject: cannot open replacement")
	}

	if _, err := lf.Write([]byte("0123456789")); err != nil {
		t.Fatalf("initial write: %v", err)
	}
	n, err := lf.Write([]byte("X"))
	if err != nil {
		t.Fatalf("write that triggers rotation returned an error (byte lost): %v", err)
	}
	if n != 1 {
		t.Fatalf("write that triggers rotation wrote %d bytes, want 1", n)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "0123456789X" {
		t.Fatalf("primary content = %q, want %q", got, "0123456789X")
	}
}

// TestLogFile_Rotate_StepThreeFailure_RetriesAfterBackoff covers the worst
// case of a step (3) failure: renaming <path>.next into place fails AND so
// does the rollback of <path>.1 back to <path> (the injected failure refuses
// every rename onto the primary path). Writes must keep landing (in what is
// now the backup file) with no error, and a later retry - once the backoff has
// elapsed - must swap in the pending replacement without repeating the rename
// to <path>.1, so the backup ends up holding exactly what was written to it
// and nothing more.
func TestLogFile_Rotate_StepThreeFailure_RetriesAfterBackoff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	lf := openLogFileForRotationTest(t, path, 10)

	clock := &fakeClock{t: time.Unix(0, 0)}
	lf.now = clock.now

	blockSwap := true
	primaryBase := filepath.Base(path)
	lf.rename = func(oldpath, newpath string) error {
		if blockSwap && filepath.Base(newpath) == primaryBase {
			// Step (3): <path>.next -> <path>.
			return errors.New("inject: step three fails")
		}
		return os.Rename(oldpath, newpath)
	}

	if _, err := lf.Write([]byte("0123456789")); err != nil {
		t.Fatalf("initial write: %v", err)
	}
	for _, b := range []byte("XYZ") {
		if _, err := lf.Write([]byte{b}); err != nil {
			t.Fatalf("write %q while onBackup: %v", b, err)
		}
	}

	backup, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != "0123456789XYZ" {
		t.Fatalf("backup during onBackup = %q, want %q", backup, "0123456789XYZ")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("primary path exists although both step three and its rollback failed: err=%v", err)
	}

	clock.advance(rotateBackoff + time.Second)
	blockSwap = false
	if _, err := lf.Write([]byte("W")); err != nil {
		t.Fatalf("write that retries the swap: %v", err)
	}

	backup, err = os.ReadFile(path + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != "0123456789XYZ" {
		t.Fatalf("backup after retry = %q, want %q unchanged (no second rename onto it)", backup, "0123456789XYZ")
	}
	primary, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(primary) != "W" {
		t.Fatalf("primary after retry = %q, want %q", primary, "W")
	}
	if _, err := os.Stat(path + ".next"); !os.IsNotExist(err) {
		t.Fatalf(".next left behind after successful retry: err=%v", err)
	}
}

// TestLogFile_Rotate_StepThreeFailure_RollsBackToPrimary asserts that when
// only step (3) fails, the rotation is rolled back so the live log stays at
// the primary path that log discovery and readers open, instead of
// disappearing until some later write happens to retry the rotation.
func TestLogFile_Rotate_StepThreeFailure_RollsBackToPrimary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	lf := openLogFileForRotationTest(t, path, 10)

	clock := &fakeClock{t: time.Unix(0, 0)}
	lf.now = clock.now

	blockSwap := true
	lf.rename = func(oldpath, newpath string) error {
		if blockSwap && oldpath == path+".next" && newpath == path {
			return errors.New("inject: step three fails")
		}
		return os.Rename(oldpath, newpath)
	}

	if _, err := lf.Write([]byte("0123456789")); err != nil {
		t.Fatalf("initial write: %v", err)
	}
	for _, b := range []byte("XYZ") {
		if _, err := lf.Write([]byte{b}); err != nil {
			t.Fatalf("write %q after the failed rotation: %v", b, err)
		}
	}

	primary, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("primary log missing after a rolled-back rotation: %v", err)
	}
	if string(primary) != "0123456789XYZ" {
		t.Fatalf("primary = %q, want %q (every byte on the live path)", primary, "0123456789XYZ")
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("backup still present after rollback: err=%v", err)
	}
	if _, err := os.Stat(path + ".next"); !os.IsNotExist(err) {
		t.Fatalf(".next left behind after rollback: err=%v", err)
	}

	// Once the backoff elapses and the filesystem recovers, a fresh rotation
	// completes normally.
	clock.advance(rotateBackoff + time.Second)
	blockSwap = false
	if _, err := lf.Write([]byte("W")); err != nil {
		t.Fatalf("write that retries the rotation: %v", err)
	}
	backup, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != "0123456789XYZ" {
		t.Fatalf("backup after recovery = %q, want %q", backup, "0123456789XYZ")
	}
	primary, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(primary) != "W" {
		t.Fatalf("primary after recovery = %q, want %q", primary, "W")
	}
}

// TestLogFile_Rotate_RetryFailure_RollsBackToPrimary covers the retry of a
// rotation stranded on the backup: when the retried step (3) fails again but
// the rollback rename now succeeds, the live log returns to the primary path
// and the stranded replacement is discarded.
func TestLogFile_Rotate_RetryFailure_RollsBackToPrimary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	lf := openLogFileForRotationTest(t, path, 10)

	clock := &fakeClock{t: time.Unix(0, 0)}
	lf.now = clock.now

	blockSwap, blockRollback := true, true
	lf.rename = func(oldpath, newpath string) error {
		if newpath == path {
			if blockSwap && oldpath == path+".next" {
				return errors.New("inject: step three fails")
			}
			if blockRollback && oldpath == path+".1" {
				return errors.New("inject: rollback fails")
			}
		}
		return os.Rename(oldpath, newpath)
	}

	if _, err := lf.Write([]byte("0123456789")); err != nil {
		t.Fatalf("initial write: %v", err)
	}
	if _, err := lf.Write([]byte("X")); err != nil {
		t.Fatalf("write that strands the rotation: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("precondition: primary should be missing while stranded: err=%v", err)
	}

	clock.advance(rotateBackoff + time.Second)
	blockRollback = false
	if _, err := lf.Write([]byte("Y")); err != nil {
		t.Fatalf("write that retries the rotation: %v", err)
	}

	primary, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("primary log missing after the retry rolled back: %v", err)
	}
	if string(primary) != "0123456789XY" {
		t.Fatalf("primary = %q, want %q", primary, "0123456789XY")
	}
	if _, err := os.Stat(path + ".next"); !os.IsNotExist(err) {
		t.Fatalf(".next left behind after rollback: err=%v", err)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("backup still present after rollback: err=%v", err)
	}
}

// TestLogFile_Rotate_StepOneFailure_PreservesExistingBackup exercises the new
// rotation mechanics: a failure creating <path>.next must leave an existing
// <path>.1 byte-identical, since nothing about <path> or <path>.1 is touched
// until the replacement is ready.
func TestLogFile_Rotate_StepOneFailure_PreservesExistingBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	const existingBackup = "existing backup\n"
	if err := os.WriteFile(path+".1", []byte(existingBackup), 0o640); err != nil {
		t.Fatal(err)
	}

	lf := openLogFileForRotationTest(t, path, 10)
	lf.openFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		return nil, errors.New("inject: cannot create .next")
	}

	if _, err := lf.Write([]byte("0123456789")); err != nil {
		t.Fatalf("initial write: %v", err)
	}
	if _, err := lf.Write([]byte("X")); err != nil {
		t.Fatalf("write that attempts rotation: %v", err)
	}

	backup, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != existingBackup {
		t.Fatalf(".1 = %q, want untouched %q", backup, existingBackup)
	}
	primary, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(primary) != "0123456789X" {
		t.Fatalf("primary = %q, want %q", primary, "0123456789X")
	}
}

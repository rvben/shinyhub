package db_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// RotateSecretsTx must serialize against a concurrent secret write the same
// way GrantSharedData and other read-then-write operations do (beginWrite /
// BEGIN IMMEDIATE), not a plain deferred transaction. A plain Begin() takes no
// write lock until the first write statement, so a concurrent writer can
// commit a change to the very row rotation already read before rotation
// writes it back; on SQLite/WAL that leaves rotation holding a snapshot the
// database has since moved past, and its own UPDATE is then rejected outright
// instead of blocking. Use a real file-backed WAL database with independent
// connections: an in-memory fixture is pinned to a single connection and
// cannot reproduce a deferred read-to-write upgrade failure (see
// deployment_contention_test.go).
func TestRotateSecretsTx_SerializesAgainstConcurrentEnvWrite(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	path := filepath.Join(t.TempDir(), "rotate.db")
	store, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}

	owner := mustCreateUser(t, store, "rotate-owner", "developer")
	app := mustCreateApp(t, store, "rotate-app", owner.ID)
	if err := store.UpsertAppEnvVar(app.ID, "SECRET_KEY", []byte("old-ciphertext"), true); err != nil {
		t.Fatal(err)
	}

	readStarted := make(chan struct{})
	proceed := make(chan struct{})
	reencryptEnv := func(v []byte) ([]byte, error) {
		close(readStarted)
		<-proceed
		return append(append([]byte{}, v...), []byte("-rotated")...), nil
	}
	passthrough := func(v []byte) ([]byte, error) { return v, nil }

	rotateDone := make(chan error, 1)
	go func() {
		_, _, _, rerr := store.RotateSecretsTx(reencryptEnv, passthrough, passthrough)
		rotateDone <- rerr
	}()

	<-readStarted

	// A concurrent write lands on the exact row rotation already read, before
	// rotation writes its own re-encrypted copy back.
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- store.UpsertAppEnvVar(app.ID, "SECRET_KEY", []byte("operator-set-value"), true)
	}()

	var writeWasImmediate bool
	select {
	case werr := <-writeDone:
		writeWasImmediate = true
		if werr != nil {
			t.Fatalf("concurrent env write failed: %v", werr)
		}
	case <-time.After(300 * time.Millisecond):
		// Blocked: rotation is holding the write lock for its whole
		// transaction, as beginWrite/BEGIN IMMEDIATE is expected to do.
	}

	close(proceed)

	rotateErr := <-rotateDone
	if !writeWasImmediate {
		if werr := <-writeDone; werr != nil {
			t.Fatalf("concurrent env write failed after unblocking: %v", werr)
		}
	}

	if rotateErr != nil {
		t.Fatalf("RotateSecretsTx must serialize against the concurrent write instead of failing: %v (concurrent write was immediate=%v)", rotateErr, writeWasImmediate)
	}
}

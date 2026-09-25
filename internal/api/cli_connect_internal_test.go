package api

import (
	"errors"
	"testing"

	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestRegisterCLIConnectRequest_RetriesOnCodeCollision proves the register
// handler's fix-closed side of the user_code UNIQUE index: a freshly
// generated code that collides with another still-pending request must not
// surface as a 500 to a CLI that did nothing wrong. The generator is
// injected so the collision is deterministic rather than needing a real
// crypto/rand clash, which at 31^8 possible codes is not practical to
// trigger for real in a test.
func TestRegisterCLIConnectRequest_RetriesOnCodeCollision(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateCLIConnectRequest("hash-taken", "ABCD-1234", "cli-existing"); err != nil {
		t.Fatal(err)
	}

	calls := 0
	generate := func() (string, error) {
		calls++
		if calls == 1 {
			return "ABCD-1234", nil // collides with the already-pending request
		}
		return "WXYZ-5678", nil
	}

	userCode, err := registerCLIConnectRequest(store, "hash-new", "cli-new", generate)
	if err != nil {
		t.Fatalf("registerCLIConnectRequest: %v", err)
	}
	if userCode != "WXYZ-5678" {
		t.Fatalf("userCode = %q, want WXYZ-5678", userCode)
	}
	if calls != 2 {
		t.Fatalf("generate called %d times, want 2 (one collision, one success)", calls)
	}

	// The request that collided must be registered under the retried code,
	// resolvable on its own, and the original pending request must still be
	// intact under its own code.
	tokenHash, name, err := store.ConsumeCLIConnectRequest("WXYZ-5678")
	if err != nil {
		t.Fatal(err)
	}
	if tokenHash != "hash-new" || name != "cli-new" {
		t.Fatalf("consumed (hash=%q, name=%q), want (hash-new, cli-new)", tokenHash, name)
	}
	tokenHash, name, err = store.ConsumeCLIConnectRequest("ABCD-1234")
	if err != nil {
		t.Fatal(err)
	}
	if tokenHash != "hash-taken" || name != "cli-existing" {
		t.Fatalf("consumed (hash=%q, name=%q), want (hash-taken, cli-existing)", tokenHash, name)
	}
}

// TestRegisterCLIConnectRequest_FailsClosedAfterMaxAttempts proves a
// generator that never stops colliding does not loop forever or return a
// code the caller cannot trust: after cliUserCodeMaxAttempts tries it fails
// with an error, not a code silently shared with someone else's request.
func TestRegisterCLIConnectRequest_FailsClosedAfterMaxAttempts(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateCLIConnectRequest("hash-taken", "ABCD-1234", "cli-existing"); err != nil {
		t.Fatal(err)
	}

	calls := 0
	generate := func() (string, error) {
		calls++
		return "ABCD-1234", nil // always collides
	}

	_, err := registerCLIConnectRequest(store, "hash-new", "cli-new", generate)
	if err == nil {
		t.Fatal("registerCLIConnectRequest succeeded against a generator that never produces a free code")
	}
	if !errors.Is(err, ErrCLIUserCodeGenerationFailed) {
		t.Fatalf("error = %v, want ErrCLIUserCodeGenerationFailed", err)
	}
	if calls != cliUserCodeMaxAttempts {
		t.Fatalf("generate called %d times, want %d (fail closed, not retry forever)", calls, cliUserCodeMaxAttempts)
	}

	// The original pending request must be untouched by the failed attempts.
	tokenHash, name, err := store.ConsumeCLIConnectRequest("ABCD-1234")
	if err != nil {
		t.Fatal(err)
	}
	if tokenHash != "hash-taken" || name != "cli-existing" {
		t.Fatalf("consumed (hash=%q, name=%q), want (hash-taken, cli-existing)", tokenHash, name)
	}
}

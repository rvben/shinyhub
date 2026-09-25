package db_test

import (
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestCLIConnectRequest_HappyPath proves the device-authorization exchange:
// a CLI registers its hash and receives back nothing usable by an attacker,
// the browser side (represented directly here by the store call the approve
// handler will make) supplies only the human-typed user code, and that code
// alone is enough to resolve the correct hash and name.
func TestCLIConnectRequest_HappyPath(t *testing.T) {
	store := dbtest.New(t)

	status, err := store.CLIConnectRequestStatus("hash-one")
	if err != nil {
		t.Fatal(err)
	}
	if status != "not_found" {
		t.Fatalf("status before registration = %q, want not_found", status)
	}

	if err := store.CreateCLIConnectRequest("hash-one", "ABCD-1234", "cli-laptop-abc123"); err != nil {
		t.Fatal(err)
	}

	status, err = store.CLIConnectRequestStatus("hash-one")
	if err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("status after registration = %q, want pending", status)
	}

	tokenHash, name, err := store.ConsumeCLIConnectRequest("ABCD-1234")
	if err != nil {
		t.Fatal(err)
	}
	if tokenHash != "hash-one" || name != "cli-laptop-abc123" {
		t.Fatalf("consumed (hash=%q, name=%q), want (hash-one, cli-laptop-abc123)", tokenHash, name)
	}
}

// TestCLIConnectRequest_UnregisteredHashNeverApproves proves the core fix:
// a hash the CLI never registered server-side cannot be approved, because
// there is nothing in cli_connect_requests to consume it against and nothing
// in api_keys yet either.
func TestCLIConnectRequest_UnregisteredHashNeverApproves(t *testing.T) {
	store := dbtest.New(t)

	status, err := store.CLIConnectRequestStatus("never-registered-hash")
	if err != nil {
		t.Fatal(err)
	}
	if status == "approved" {
		t.Fatal("an unregistered hash must never report approved")
	}
	if status != "not_found" {
		t.Fatalf("status for an unregistered hash = %q, want not_found", status)
	}
}

// TestCLIConnectRequest_WrongCodeFails proves a code that does not match any
// pending request is rejected, distinguishably from neither a wrong-slug nor
// an internal error: it is the same ErrNotFound used for expiry and replay,
// which is deliberate (see TestCLIConnectRequest_ConsumedAndExpiredIndistinguishable).
func TestCLIConnectRequest_WrongCodeFails(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateCLIConnectRequest("hash-one", "ABCD-1234", "cli-laptop-abc123"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := store.ConsumeCLIConnectRequest("WRONG-CODE"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("wrong code error = %v, want ErrNotFound", err)
	}

	// The correct code still works afterward: a wrong guess does not burn the
	// pending request.
	tokenHash, _, err := store.ConsumeCLIConnectRequest("ABCD-1234")
	if err != nil {
		t.Fatal(err)
	}
	if tokenHash != "hash-one" {
		t.Fatalf("tokenHash = %q, want hash-one", tokenHash)
	}
}

// TestCLIConnectRequest_ReplayFails proves single use: consuming the same
// code twice fails the second time.
func TestCLIConnectRequest_ReplayFails(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateCLIConnectRequest("hash-one", "ABCD-1234", "cli-laptop-abc123"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ConsumeCLIConnectRequest("ABCD-1234"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ConsumeCLIConnectRequest("ABCD-1234"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("replay error = %v, want ErrNotFound", err)
	}
}

// TestCLIConnectRequest_ExpiredRequestCannotBeConsumed proves the TTL is
// enforced against the database clock, not just at creation time: a request
// backdated past the TTL reports "expired" and can no longer be approved,
// even though it still exists in the table (lazy cleanup, not a background
// job, matches the oauth_states/app_launch_codes convention).
func TestCLIConnectRequest_ExpiredRequestCannotBeConsumed(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateCLIConnectRequest("hash-one", "ABCD-1234", "cli-laptop-abc123"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(
		`UPDATE cli_connect_requests SET created_at = ? WHERE token_hash = ?`,
		time.Now().UTC().Add(-(db.CLIConnectRequestTTL + time.Minute)), "hash-one",
	); err != nil {
		t.Fatal(err)
	}

	status, err := store.CLIConnectRequestStatus("hash-one")
	if err != nil {
		t.Fatal(err)
	}
	if status != "expired" {
		t.Fatalf("status for a backdated request = %q, want expired", status)
	}

	if _, _, err := store.ConsumeCLIConnectRequest("ABCD-1234"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("consume of an expired request error = %v, want ErrNotFound", err)
	}
}

// TestCLIConnectRequest_DuplicateUserCodeIsRejected proves the UNIQUE index
// added on cli_connect_requests.user_code: two pending requests must never
// share a code, because ConsumeCLIConnectRequest resolves a request by code
// alone. Without the constraint this insert would silently succeed and leave
// two rows a single `DELETE ... WHERE user_code = ? RETURNING` could match,
// binding one CLI's approval to the other's identity and dropping its own
// request with no error to either side.
func TestCLIConnectRequest_DuplicateUserCodeIsRejected(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateCLIConnectRequest("hash-one", "ABCD-1234", "cli-laptop-abc123"); err != nil {
		t.Fatal(err)
	}

	err := store.CreateCLIConnectRequest("hash-two", "ABCD-1234", "cli-desktop-def456")
	if !errors.Is(err, db.ErrCLIUserCodeExists) {
		t.Fatalf("duplicate user_code error = %v, want ErrCLIUserCodeExists", err)
	}

	// The first registration is untouched: a rejected duplicate must not have
	// deleted or altered the row it collided with.
	tokenHash, name, err := store.ConsumeCLIConnectRequest("ABCD-1234")
	if err != nil {
		t.Fatal(err)
	}
	if tokenHash != "hash-one" || name != "cli-laptop-abc123" {
		t.Fatalf("consumed (hash=%q, name=%q), want (hash-one, cli-laptop-abc123)", tokenHash, name)
	}
}

// TestCLIConnectRequest_ApprovedStatusComesFromAPIKeys proves that once the
// approve handler has created the API key (modeled here directly through
// CreateAPIKey, since that is the store call the handler makes), the status
// the waiting CLI polls for flips to "approved" independently of the now-
// consumed pending request row.
func TestCLIConnectRequest_ApprovedStatusComesFromAPIKeys(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: "!disabled", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	user, err := store.GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateCLIConnectRequest("hash-one", "ABCD-1234", "cli-laptop-abc123"); err != nil {
		t.Fatal(err)
	}
	tokenHash, name, err := store.ConsumeCLIConnectRequest("ABCD-1234")
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().UTC().Add(90 * 24 * time.Hour)
	if _, _, err := store.CreateAPIKey(db.CreateAPIKeyParams{UserID: user.ID, KeyHash: tokenHash, Name: name, ExpiresAt: &expiresAt}); err != nil {
		t.Fatal(err)
	}

	status, err := store.CLIConnectRequestStatus("hash-one")
	if err != nil {
		t.Fatal(err)
	}
	if status != "approved" {
		t.Fatalf("status after approval = %q, want approved", status)
	}
}

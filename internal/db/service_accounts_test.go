package db_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestDeploymentServiceAccountFailsClosedOnHumanCollision(t *testing.T) {
	store := dbtest.New(t)
	if _, err := store.DB().Exec(`INSERT INTO users (username, password_hash, role) VALUES ('__deploy__', 'human-password', 'admin')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer"); !errors.Is(err, db.ErrServiceAccountCollision) {
		t.Fatalf("collision error = %v", err)
	}
}

func TestSyncDeployCredentialRotatesInPlaceAndIsConfigurationManaged(t *testing.T) {
	store := dbtest.New(t)
	account, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.SyncDeployCredential(account.ID, "hash-one", "developer", []string{"sales"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.SyncDeployCredential(account.ID, "hash-two", "operator", nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.CredentialRole != "operator" || !second.Unrestricted {
		t.Fatalf("rotated credential = %+v, first = %+v", second, first)
	}
	if _, _, err := store.AuthenticateAPIKey("hash-one"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("old hash should be revoked, got %v", err)
	}
	user, key, err := store.AuthenticateAPIKey("hash-two")
	if err != nil || user.ServiceAccountKey != "deployment" || key.ManagedBy != "configuration" {
		t.Fatalf("new credential user=%+v key=%+v err=%v", user, key, err)
	}
	if err := store.DeleteServiceCredential(first.ID, account.ID); !errors.Is(err, db.ErrManagedCredential) {
		t.Fatalf("managed delete = %v", err)
	}
	if err := store.DeleteDeployCredential(); err != nil {
		t.Fatalf("delete configured credential: %v", err)
	}
	if _, _, err := store.AuthenticateAPIKey("hash-two"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("removed configuration credential should not authenticate, got %v", err)
	}
}

func TestCreateUserRejectsReservedDeploymentUsername(t *testing.T) {
	store := dbtest.New(t)
	err := store.CreateUser(db.CreateUserParams{Username: db.SystemUsernameDeploy, PasswordHash: "x", Role: "admin"})
	if !errors.Is(err, db.ErrReservedUsername) {
		t.Fatalf("error = %v", err)
	}
}

func TestServiceCredentialNamesAreUniquePerAccount(t *testing.T) {
	store := dbtest.New(t)
	account, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	if err != nil {
		t.Fatal(err)
	}
	params := db.CreateAPIKeyParams{UserID: account.ID, KeyHash: "unique-name-one", Name: "production CI",
		CredentialType: "service", CredentialRole: "developer", AppScope: []string{"sales"}}
	if _, _, err := store.CreateAPIKey(params); err != nil {
		t.Fatal(err)
	}
	params.KeyHash = "unique-name-two"
	if _, _, err := store.CreateAPIKey(params); !errors.Is(err, db.ErrAPIKeyNameExists) {
		t.Fatalf("duplicate name error = %v, want ErrAPIKeyNameExists", err)
	}
}

func TestServiceCredentialNameCheckIsAtomic(t *testing.T) {
	store := dbtest.New(t)
	account, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, hash := range []string{"concurrent-name-one", "concurrent-name-two"} {
		wg.Add(1)
		go func(hash string) {
			defer wg.Done()
			<-start
			_, _, err := store.CreateAPIKey(db.CreateAPIKeyParams{UserID: account.ID, KeyHash: hash, Name: "production CI",
				CredentialType: "service", CredentialRole: "developer", AppScope: []string{"sales"}})
			errs <- err
		}(hash)
	}
	close(start)
	wg.Wait()
	close(errs)
	var created, conflicts int
	for err := range errs {
		switch {
		case err == nil:
			created++
		case errors.Is(err, db.ErrAPIKeyNameExists):
			conflicts++
		default:
			t.Fatalf("concurrent create error = %v", err)
		}
	}
	if created != 1 || conflicts != 1 {
		t.Fatalf("created=%d conflicts=%d, want 1 and 1", created, conflicts)
	}
}

func TestDeployTokenNameIsReservedWithoutBlockingLegacyReconciliation(t *testing.T) {
	store := dbtest.New(t)
	account, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateAPIKey(db.CreateAPIKeyParams{UserID: account.ID, KeyHash: "reserved-api", Name: " shinyhub_deploy_token ",
		CredentialType: "service", CredentialRole: "developer", Unrestricted: true}); !errors.Is(err, db.ErrReservedCredentialName) {
		t.Fatalf("reserved name error = %v, want ErrReservedCredentialName", err)
	}

	// Model a database created before the name became reserved. Configuration
	// reconciliation keys on external_id, so this legacy display-name collision
	// must not prevent the managed credential from being installed.
	if _, err := store.DB().Exec(`INSERT INTO api_keys
		(user_id, key_hash, name, credential_type, credential_role, app_scope, unrestricted)
		VALUES (?, ?, ?, 'service', 'developer', '[]', ?)`, account.ID, "legacy-reserved-name", db.DeployTokenCredentialName, true); err != nil {
		t.Fatal(err)
	}
	managed, err := store.SyncDeployCredential(account.ID, "managed-reserved-name", "developer", nil)
	if err != nil {
		t.Fatalf("sync deploy credential: %v", err)
	}
	if managed.ExternalID != db.DeployTokenExternalID || managed.Name != db.DeployTokenCredentialName {
		t.Fatalf("managed credential = %+v", managed)
	}
	if _, _, err := store.AuthenticateAPIKey("legacy-reserved-name"); err != nil {
		t.Fatalf("legacy credential was not preserved: %v", err)
	}
	if _, _, err := store.AuthenticateAPIKey("managed-reserved-name"); err != nil {
		t.Fatalf("managed credential does not authenticate: %v", err)
	}
}

func TestSyncDeployCredentialAdoptsAnExistingAPIKeySecret(t *testing.T) {
	store := dbtest.New(t)
	account, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.SyncDeployCredential(account.ID, "original-managed-secret", "developer", []string{"sales"})
	if err != nil {
		t.Fatal(err)
	}
	adoptedID, _, err := store.CreateAPIKey(db.CreateAPIKeyParams{UserID: account.ID, KeyHash: "reused-api-secret", Name: "move to environment",
		CredentialType: "service", CredentialRole: "viewer", AppScope: []string{"finance"}})
	if err != nil {
		t.Fatal(err)
	}

	managed, err := store.SyncDeployCredential(account.ID, "reused-api-secret", "operator", nil)
	if err != nil {
		t.Fatalf("adopt existing API credential: %v", err)
	}
	if managed.ID != adoptedID || managed.ID == first.ID || managed.ExternalID != db.DeployTokenExternalID {
		t.Fatalf("managed credential = %+v, adopted id=%d old managed id=%d", managed, adoptedID, first.ID)
	}
	if _, _, err := store.AuthenticateAPIKey("original-managed-secret"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("superseded managed secret should be revoked, got %v", err)
	}
	user, key, err := store.AuthenticateAPIKey("reused-api-secret")
	if err != nil || user.ID != account.ID || key.ID != adoptedID || key.ManagedBy != "configuration" || key.CredentialRole != "operator" {
		t.Fatalf("adopted user=%+v key=%+v err=%v", user, key, err)
	}
}

package fargate

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
)

func TestSecretName_IncludesPrefixAndAppID(t *testing.T) {
	// The name must include the install prefix and the numeric app id (not just
	// the slug) so a delete-then-recreate of the same slug, or two installs
	// sharing an account, never collide.
	got := SecretName("shinyhub/prod", 42, "AWS_SECRET")
	want := "shinyhub/prod/app-42/AWS_SECRET"
	if got != want {
		t.Errorf("SecretName = %q, want %q", got, want)
	}
}

func TestSecretName_DifferentAppsDoNotCollide(t *testing.T) {
	a := SecretName("p", 1, "K")
	b := SecretName("p", 2, "K")
	if a == b {
		t.Errorf("apps 1 and 2 produced the same secret name %q", a)
	}
}

// fakeSM is a scriptable secretsManagerAPI.
type fakeSM struct {
	createFn func(*secretsmanager.CreateSecretInput) (*secretsmanager.CreateSecretOutput, error)
	putFn    func(*secretsmanager.PutSecretValueInput) (*secretsmanager.PutSecretValueOutput, error)
	deleteFn func(*secretsmanager.DeleteSecretInput) (*secretsmanager.DeleteSecretOutput, error)
}

func (f *fakeSM) CreateSecret(_ context.Context, in *secretsmanager.CreateSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
	return f.createFn(in)
}
func (f *fakeSM) PutSecretValue(_ context.Context, in *secretsmanager.PutSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
	return f.putFn(in)
}
func (f *fakeSM) DeleteSecret(_ context.Context, in *secretsmanager.DeleteSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error) {
	return f.deleteFn(in)
}

func TestSecretsManagerStore_Put_CreatesNewSecret(t *testing.T) {
	var created *secretsmanager.CreateSecretInput
	sm := &fakeSM{
		createFn: func(in *secretsmanager.CreateSecretInput) (*secretsmanager.CreateSecretOutput, error) {
			created = in
			return &secretsmanager.CreateSecretOutput{ARN: aws.String("arn:aws:secretsmanager:eu-west-1:111122223333:secret:p/app-1/K-AbCdEf")}, nil
		},
	}
	store := newSecretsManagerStore(sm, "")
	arn, err := store.Put(context.Background(), "p/app-1/K", "val")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if arn != "arn:aws:secretsmanager:eu-west-1:111122223333:secret:p/app-1/K-AbCdEf" {
		t.Errorf("arn = %q", arn)
	}
	if created == nil || aws.ToString(created.Name) != "p/app-1/K" || aws.ToString(created.SecretString) != "val" {
		t.Errorf("CreateSecret got %+v", created)
	}
}

func TestSecretsManagerStore_Put_UpdatesExistingSecret(t *testing.T) {
	var putCalled bool
	sm := &fakeSM{
		createFn: func(*secretsmanager.CreateSecretInput) (*secretsmanager.CreateSecretOutput, error) {
			return nil, &smtypes.ResourceExistsException{Message: aws.String("already exists")}
		},
		putFn: func(in *secretsmanager.PutSecretValueInput) (*secretsmanager.PutSecretValueOutput, error) {
			putCalled = true
			return &secretsmanager.PutSecretValueOutput{ARN: aws.String("arn:aws:secretsmanager:eu-west-1:111122223333:secret:p/app-1/K-AbCdEf")}, nil
		},
	}
	store := newSecretsManagerStore(sm, "")
	arn, err := store.Put(context.Background(), "p/app-1/K", "val2")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !putCalled {
		t.Error("expected PutSecretValue to be called when the secret already exists")
	}
	if arn == "" {
		t.Error("expected a non-empty ARN from PutSecretValue")
	}
}

func TestSecretsManagerStore_Put_PassesKMSKey(t *testing.T) {
	var created *secretsmanager.CreateSecretInput
	sm := &fakeSM{
		createFn: func(in *secretsmanager.CreateSecretInput) (*secretsmanager.CreateSecretOutput, error) {
			created = in
			return &secretsmanager.CreateSecretOutput{ARN: aws.String("arn")}, nil
		},
	}
	store := newSecretsManagerStore(sm, "alias/shinyhub")
	if _, err := store.Put(context.Background(), "n", "v"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if aws.ToString(created.KmsKeyId) != "alias/shinyhub" {
		t.Errorf("KmsKeyId = %q, want alias/shinyhub", aws.ToString(created.KmsKeyId))
	}
}

func TestSecretsManagerStore_Delete_ForcesImmediateDeletion(t *testing.T) {
	var del *secretsmanager.DeleteSecretInput
	sm := &fakeSM{
		deleteFn: func(in *secretsmanager.DeleteSecretInput) (*secretsmanager.DeleteSecretOutput, error) {
			del = in
			return &secretsmanager.DeleteSecretOutput{}, nil
		},
	}
	store := newSecretsManagerStore(sm, "")
	if err := store.Delete(context.Background(), "p/app-1/K"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Force-delete avoids the 7-30 day recovery window that would otherwise
	// reserve the name and block an immediate delete-then-recreate of the slug.
	if del == nil || !aws.ToBool(del.ForceDeleteWithoutRecovery) {
		t.Errorf("DeleteSecret should force immediate deletion, got %+v", del)
	}
}

func TestSecretsManagerStore_Delete_ToleratesMissingSecret(t *testing.T) {
	sm := &fakeSM{
		deleteFn: func(*secretsmanager.DeleteSecretInput) (*secretsmanager.DeleteSecretOutput, error) {
			return nil, &smtypes.ResourceNotFoundException{Message: aws.String("not found")}
		},
	}
	store := newSecretsManagerStore(sm, "")
	if err := store.Delete(context.Background(), "missing"); err != nil {
		t.Errorf("Delete of a missing secret must be a no-op, got %v", err)
	}
}

func TestSecretsManagerStore_Put_PropagatesUnexpectedError(t *testing.T) {
	sm := &fakeSM{
		createFn: func(*secretsmanager.CreateSecretInput) (*secretsmanager.CreateSecretOutput, error) {
			return nil, errors.New("access denied")
		},
	}
	store := newSecretsManagerStore(sm, "")
	if _, err := store.Put(context.Background(), "n", "v"); err == nil {
		t.Error("expected Put to propagate an unexpected error")
	}
}

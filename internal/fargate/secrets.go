package fargate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
)

// SecretsStore stores an app's secret env values out of band so they can be
// referenced from a task definition by ARN instead of carried as plaintext task
// overrides. Put is an upsert that returns the secret's ARN; Delete removes it
// and is a no-op when the secret is already gone; DeleteByPrefix removes every
// secret whose name begins with the given prefix (used for app-delete cleanup).
type SecretsStore interface {
	Put(ctx context.Context, name, value string) (arn string, err error)
	Delete(ctx context.Context, name string) error
	DeleteByPrefix(ctx context.Context, prefix string) error
}

// SecretName builds the store name for one app secret. It namespaces by an
// operator-chosen install prefix and the numeric app id (not the slug) so a
// delete-then-recreate of the same slug, or two installs sharing one AWS
// account, never collide. Example: "shinyhub/prod/app-42/AWS_SECRET".
func SecretName(prefix string, appID int64, key string) string {
	return fmt.Sprintf("%s/app-%d/%s", prefix, appID, key)
}

// secretsManagerAPI is the subset of the AWS Secrets Manager API the store
// needs. The SDK's *secretsmanager.Client satisfies it; tests supply a fake.
type secretsManagerAPI interface {
	CreateSecret(ctx context.Context, in *secretsmanager.CreateSecretInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error)
	PutSecretValue(ctx context.Context, in *secretsmanager.PutSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error)
	DeleteSecret(ctx context.Context, in *secretsmanager.DeleteSecretInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error)
	ListSecrets(ctx context.Context, in *secretsmanager.ListSecretsInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error)
}

// secretsManagerStore is a SecretsStore backed by AWS Secrets Manager.
type secretsManagerStore struct {
	api secretsManagerAPI
	// kmsKeyID, when non-empty, encrypts secrets with a customer-managed KMS
	// key (id, ARN, or alias) instead of the default aws/secretsmanager key.
	kmsKeyID string
}

// newSecretsManagerStore builds a SecretsStore over the given API. kmsKeyID is
// optional; empty uses the account's default Secrets Manager KMS key.
func newSecretsManagerStore(api secretsManagerAPI, kmsKeyID string) *secretsManagerStore {
	return &secretsManagerStore{api: api, kmsKeyID: kmsKeyID}
}

// NewSecretsManagerStore builds a SecretsStore backed by the AWS Secrets Manager
// client. kmsKeyID is optional (empty uses the default aws/secretsmanager key).
// Wire the result with WithSecretsStore.
func NewSecretsManagerStore(client *secretsmanager.Client, kmsKeyID string) SecretsStore {
	return newSecretsManagerStore(client, kmsKeyID)
}

// Put upserts the secret: it creates it, or (when it already exists) writes a
// new value. It returns the secret's ARN, which is what a task definition's
// secrets block references via valueFrom.
func (s *secretsManagerStore) Put(ctx context.Context, name, value string) (string, error) {
	in := &secretsmanager.CreateSecretInput{
		Name:         aws.String(name),
		SecretString: aws.String(value),
	}
	if s.kmsKeyID != "" {
		in.KmsKeyId = aws.String(s.kmsKeyID)
	}
	out, err := s.api.CreateSecret(ctx, in)
	if err == nil {
		return aws.ToString(out.ARN), nil
	}
	var exists *smtypes.ResourceExistsException
	if !errors.As(err, &exists) {
		return "", fmt.Errorf("create secret %s: %w", name, err)
	}
	// Already exists: write a new version. PutSecretValue keeps the existing
	// KMS key, so the key is set only at creation time.
	pv, perr := s.api.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId:     aws.String(name),
		SecretString: aws.String(value),
	})
	if perr != nil {
		return "", fmt.Errorf("put secret value %s: %w", name, perr)
	}
	return aws.ToString(pv.ARN), nil
}

// Delete removes the secret immediately (force, no recovery window) so the name
// is free to reuse at once. A missing secret is treated as already deleted.
func (s *secretsManagerStore) Delete(ctx context.Context, name string) error {
	_, err := s.api.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId:                   aws.String(name),
		ForceDeleteWithoutRecovery: aws.Bool(true),
	})
	if err == nil {
		return nil
	}
	var notFound *smtypes.ResourceNotFoundException
	if errors.As(err, &notFound) {
		return nil
	}
	return fmt.Errorf("delete secret %s: %w", name, err)
}

// DeleteByPrefix removes every secret whose name begins with prefix. Secrets
// Manager's "name" filter is itself a prefix match, but each result is verified
// with an exact HasPrefix check so a sibling app (e.g. ".../app-1/" vs
// ".../app-11/") can never be deleted by mistake. Paginates the full result.
func (s *secretsManagerStore) DeleteByPrefix(ctx context.Context, prefix string) error {
	var next *string
	for {
		out, err := s.api.ListSecrets(ctx, &secretsmanager.ListSecretsInput{
			Filters: []smtypes.Filter{{
				Key:    smtypes.FilterNameStringTypeName,
				Values: []string{prefix},
			}},
			NextToken: next,
		})
		if err != nil {
			return fmt.Errorf("list secrets %s*: %w", prefix, err)
		}
		for _, sec := range out.SecretList {
			name := aws.ToString(sec.Name)
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			if derr := s.Delete(ctx, name); derr != nil {
				return derr
			}
		}
		if out.NextToken == nil || aws.ToString(out.NextToken) == "" {
			return nil
		}
		next = out.NextToken
	}
}

var _ SecretsStore = (*secretsManagerStore)(nil)

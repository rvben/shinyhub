// Package appenv turns an app's stored environment variables into the
// "KEY=VALUE" slices the runtime injects at start time.
//
// It is the single source of truth for the secret/non-secret distinction:
// non-secret values are emitted as-is; secret values (AppEnvVar.IsSecret) are
// decrypted and returned in a SEPARATE slice so callers can route them
// differently from plaintext env. Today every runtime still injects the secret
// values as plaintext: the native, Docker, and Fargate runtimes concatenate the
// two slices into one process/container environment (no behavioral difference
// from a single flat slice, since a given key is either secret or not), so on
// Fargate the values currently remain visible via ecs:DescribeTasks. Returning
// them separately is what will later let the Fargate runtime route secrets
// through the task definition's secrets block instead of plaintext task
// overrides.
//
// Decryption fails closed: if any secret cannot be decrypted (wrong/rotated
// key, truncated or tampered ciphertext) Resolve returns an error and no env at
// all, rather than launching an app with a missing or ciphertext value.
package appenv

import (
	"fmt"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/secrets"
)

// Resolve partitions vars into non-secret env and decrypted secret env, each as
// "KEY=VALUE" strings in input order. secretsKey is the AES key (see
// secrets.DeriveKey) used to decrypt secret values. A decrypt failure aborts
// with an error and nil slices (fail closed).
func Resolve(vars []db.AppEnvVar, secretsKey []byte) (env []string, secretEnv []string, err error) {
	for _, v := range vars {
		if v.IsSecret {
			plain, derr := secrets.Decrypt(secretsKey, v.Value)
			if derr != nil {
				return nil, nil, fmt.Errorf("decrypt secret env %q: %w", v.Key, derr)
			}
			secretEnv = append(secretEnv, v.Key+"="+string(plain))
			continue
		}
		env = append(env, v.Key+"="+string(v.Value))
	}
	return env, secretEnv, nil
}

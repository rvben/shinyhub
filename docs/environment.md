# Environment Variables and Secrets

Every app has its own key-value environment store. Non-secret values are stored
in plaintext; values marked `--secret` are encrypted at rest with AES-256-GCM
(the key is derived from `SHINYHUB_AUTH_SECRET` via HKDF-SHA256) and can never
be read back through the API or UI.

## When to use env vars vs persistent data

| You want to... | Use |
|---|---|
| Configure a cloud bucket URL, DB URL, or API endpoint | Env var (non-secret) |
| Pass a password, API key, or private-key string | Env var (secret) |
| Ship a Parquet / DuckDB / SQLite file the app reads | [Persistent data dir](data.md) |
| Let the app write uploads, cache, or session data | [Persistent data dir](data.md) |

## CLI

```bash
shinyhub env set demo AWS_REGION=eu-west-1
shinyhub env set demo AWS_SECRET_ACCESS_KEY --secret --stdin   # value from stdin
shinyhub env set demo LOG_LEVEL=debug --restart                # restart the app after setting
shinyhub env ls demo
shinyhub env rm demo OLD_VAR
```

Keys must match `[A-Z_][A-Z0-9_]*`. Values are capped at 64 KiB each, with at
most 100 keys per app.

## UI

Open an app's **Settings** modal and switch to the **Environment** tab to list,
add, edit, and delete variables. Secret values are masked in the list and are
write-only once created.

## Reserved prefix

Keys starting with `SHINYHUB_` are reserved for platform variables
(`SHINYHUB_APP_DATA`, and future additions) and are rejected with a 422.

## Caveat: rotating `SHINYHUB_AUTH_SECRET`

The encryption key is derived from `SHINYHUB_AUTH_SECRET`. Rotating that secret
invalidates every stored secret value: the affected apps fail to read their
secrets until the variables are re-set via the CLI or UI.

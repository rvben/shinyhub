# Rotating `auth.secret`

`auth.secret` is the root of every secret ShinyHub stores. It is HKDF-derived
(domain-separated) into:

- the AES-256-GCM key that encrypts **app environment secrets** at rest,
- the key that encrypts the **worker CA private key** at rest (HA / off-host
  workers),
- short-lived signing keys for identity JWTs and sticky/session cookies.

Changing `auth.secret` therefore invalidates everything encrypted under the old
value. The short-lived signing keys self-heal (tokens and cookies are re-minted
within their TTL), but the **at-rest** secrets - app env secrets and the worker
CA key - do not: after a naive change they can no longer be decrypted, and the
affected apps fail to start. That makes an otherwise routine response to a
suspected leak a data-loss event.

`shinyhub rotate-secret` closes that gap: it re-encrypts every at-rest secret
from the current `auth.secret` to a new one, atomically.

## When to rotate

- The `auth.secret` value may have leaked (logs, a shared `.env`, a backup that
  left your control).
- Routine key hygiene.

## Procedure

Run with the **server stopped** (it is a maintenance operation, like `backup` /
`restore`), so nothing writes new secrets under the old key mid-rotation.

```bash
# 1. Generate the new secret.
NEW=$(openssl rand -hex 32)

# 2. Stop the server.
systemctl stop shinyhub          # or however you run it

# 3. Re-encrypt all at-rest secrets. The CURRENT secret is read the usual way
#    (config / SHINYHUB_AUTH_SECRET); the NEW one via SHINYHUB_NEW_AUTH_SECRET.
SHINYHUB_NEW_AUTH_SECRET="$NEW" shinyhub rotate-secret --config /etc/shinyhub/shinyhub.yaml

# 4. Switch the server to the new secret (update the config file or the
#    SHINYHUB_AUTH_SECRET env used by the unit) and start it.
systemctl start shinyhub
```

`rotate-secret` re-encrypts in a **single database transaction**: if anything
fails (a wrong current secret, a DB error) nothing is committed, so it is safe
to retry - you never end up half old / half new. On success it reports how many
app-env secrets were re-encrypted and whether the worker CA key was rotated.

## Notes

- The command refuses a new secret shorter than 32 characters, the example
  placeholder, or one identical to the current secret.
- Take a `shinyhub backup` first if you want a rollback point; the rotation only
  touches the encrypted columns, but a backup is cheap insurance.
- Non-secret env vars are stored in plaintext and are left untouched.
- HA: run the rotation once against the shared database; the worker CA key lives
  in the database (not on disk), so all control-plane instances pick up the
  re-encrypted key when they restart on the new secret.

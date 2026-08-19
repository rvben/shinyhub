---
description: "auth.secret is HKDF-derived into every key ShinyHub stores, so follow this procedure or at-rest secrets stop decrypting after a change."
---

# Rotating `auth.secret`

`auth.secret` is the root of every secret ShinyHub stores. It is HKDF-derived
(domain-separated) into:

- the AES-256-GCM key that encrypts **app environment secrets** at rest,
- the key that encrypts the **worker CA private key** at rest (HA / off-host
  workers),
- short-lived signing keys for identity JWTs and sticky/session cookies.

Changing `auth.secret` therefore invalidates everything encrypted under the old
value. Sticky and session cookies recover on their own (a user signs in again
and gets a cookie under the new key), but the **at-rest** secrets - app env
secrets and the worker CA key - do not: after a naive change they can no longer
be decrypted, and the affected apps fail to start. That makes an otherwise
routine response to a suspected leak a data-loss event.

Identity JWTs do not recover on their own either, for a different reason: an
app process receives its `SHINYHUB_IDENTITY_KEY` in its environment when it is
**spawned**, and a running app keeps the key it started with (the server
re-adopts live app processes across its own restart). Until each app is
restarted, the server mints tokens under the new key that the app rejects, and
the client helpers report `bad_signature`. Step 5 below is part of the
rotation, not optional cleanup.

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

# 5. Restart every app so each picks up its new identity key. A process that
#    was already running keeps the key it was spawned with.
for slug in $(shinyhub apps list --json | jq -r '.items[].slug'); do
  shinyhub apps restart "$slug"
done
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

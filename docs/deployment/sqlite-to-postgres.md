# Migrating from SQLite to Postgres

ShinyHub runs single-node on SQLite by default. High availability (multiple
control-plane instances) requires the Postgres backend. `shinyhub
migrate-backend` copies an existing single-node SQLite database into a fresh
Postgres database so you can adopt HA without rebuilding your users, apps,
deployments, and secrets by hand.

## What it does

- Migrates the **target** Postgres database to the current schema, then copies
  every data table from the SQLite **source**, preserving row IDs and all
  foreign-key references, in a **single transaction**. If anything fails,
  nothing is committed.
- Values are coerced to the target's real column types (e.g. SQLite's text/epoch
  timestamps become Postgres `timestamptz`), and per-table id sequences are
  reset so future inserts do not collide with migrated IDs.
- Encrypted columns (app-env secrets, the worker CA key) are copied verbatim, so
  they still decrypt under the **same** `auth.secret`. Keep `auth.secret`
  unchanged across the migration (or rotate it separately - see
  [secret-rotation.md](../secret-rotation.md)).

## Prerequisites

- A running Postgres and an **empty** target database (the command refuses a
  target that already has users or apps, so it never clobbers a deployment).
- The connecting Postgres role must be able to `SET session_replication_role`
  (i.e. a superuser or a role with that privilege) - the copy disables FK
  triggers for the bulk load. A freshly created database you own satisfies this.

## Procedure

Run with the server **stopped**.

```bash
# 1. Stop the single-node server.
systemctl stop shinyhub

# 2. Copy SQLite -> Postgres. The SOURCE is the SQLite DB in your config; the
#    TARGET is the new Postgres DSN.
shinyhub migrate-backend \
  --config /etc/shinyhub/shinyhub.yaml \
  --to 'postgres://shinyhub:pass@db-host:5432/shinyhub?sslmode=require'
# (or set SHINYHUB_TARGET_DSN instead of --to)

# 3. Point the server at Postgres: set database.dsn (or SHINYHUB_DB_DSN) to the
#    Postgres DSN, keeping the SAME auth.secret. Then start the server(s).
systemctl start shinyhub
```

The command reports how many rows across how many tables it copied. Because it
is a single transaction and refuses a non-empty target, it is safe to retry: a
failed run leaves the target unchanged.

## Notes

- Take a `shinyhub backup` of the SQLite side first if you want a rollback point.
  The migration only reads the source; it never modifies it.
- The SQLite database is left intact - you can keep it as your rollback until the
  Postgres deployment is proven.
- Transient tables (rate-limit counters, OAuth-state nonces, live session rows)
  are copied too, but they are short-lived and repopulate on their own.
- For the HA topology this unblocks, see
  [ha-data-plane.md](ha-data-plane.md).

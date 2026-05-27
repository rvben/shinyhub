# Persistent Data Dir

Every deployed app gets its own directory at `<storage.app_data_dir>/<slug>/`.
Use it for files the app reads (Parquet, DuckDB, SQLite) and for data the app
writes (uploads, cache, session state). For configuration and secrets, use
[environment variables](environment.md) instead.

## How the app sees it

The path is exposed to the app process two ways:

- `SHINYHUB_APP_DATA` env var (absolute path).
- `./data/` symlink inside the app's working directory (or, under
  `runtime.mode: docker`, a bind mount to `/app-data` plus a `<workdir>/data`
  symlink).

The data dir survives deploys, restarts, and rollbacks. It is removed only when
the app itself is deleted. Recreating an app with the same slug starts with a
fresh, empty data dir.

## Pushing data

Deploy bundles must not contain a `data/` entry: the server rejects any bundle
whose first path segment is a file, directory, or symlink named `data` (a 422
with the offending path). Push data in separately:

```bash
shinyhub data push <slug> ./seed.parquet
shinyhub data push <slug> ./big.csv --dest datasets/2026.csv --restart
shinyhub data ls   <slug>
shinyhub data rm   <slug> stale.csv
```

The same operations are available from the UI under **Settings -> Data**.

## Authorization

`PUT` and `DELETE` on `/api/apps/:slug/data/*path` require app `manager` rights
or platform `admin` / `operator`.

`GET /api/apps/:slug/data` requires the app's owner, an explicit member (any
role), or a platform admin / operator. **Public or shared visibility alone is
not enough**: file listings can leak business intent (`q4-revenue.parquet`) and
are kept off the public surface even when the app itself is public.

## Quota

`storage.app_quota_mb` caps the combined on-disk footprint of the app's deploy
bundles plus its data dir. The check runs on every `PUT` and is
overwrite-aware: replacing a 100 MB file with a 50 MB one always succeeds. Set
it to `0` to disable.

## Concurrent writes

The persistent dir is safe for any number of concurrent **readers**. For
concurrent **writers**, use a real database (Postgres or MySQL); local SQLite
or DuckDB in read-write mode does not survive multi-process writes.

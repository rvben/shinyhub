---
description: "Give each app a persistent directory that survives redeploys, for the Parquet, DuckDB, and SQLite files it reads and writes."
---

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

## Reading data back

`data pull` is the read half of `data push`. It writes to the basename of the
remote path in the current directory, or wherever `--dest` names, and refuses to
overwrite an existing local file unless you pass `--force`:

```bash
shinyhub data pull <slug> datasets/2026.csv
shinyhub data pull <slug> seed.parquet --dest /tmp/check.parquet
shinyhub data pull <slug> seed.parquet --dest - | sha256sum
```

Every pull reports the sha256 of the bytes that arrived, which is what makes it
a verification rather than a copy. After restoring a server from backup,
`data ls` shows that a file came back with the right name and size; comparing
this digest against the one `data push` reported when the file was uploaded is
what shows the file itself came back. A pull that is cut short writes nothing:
the download lands in a temporary file and is renamed only once it is complete,
so a truncated file never appears under the name you asked for.

With `--dest -` the file's bytes are the only thing on stdout and the summary
goes to stderr, so piping into a checksum stays clean.

## Authorization

`PUT` and `DELETE` on `/api/apps/:slug/data/*path` require app `manager` rights
or platform `admin` / `operator`. So does `GET /api/apps/:slug/data/*path`,
the single-file download: contents sit with the same permission that can
overwrite or delete them.

`GET /api/apps/:slug/data` (the listing) is deliberately looser: it requires the
app's owner, an explicit member (any role), or a platform admin / operator.
**Public or shared visibility alone is not enough**: file listings can leak
business intent (`q4-revenue.parquet`) and are kept off the public surface even
when the app itself is public. An explicit viewer-member can therefore see that
`q4-revenue.parquet` exists and how big it is, but cannot read what is in it.

Downloads are recorded in the audit log as `data.pull`. It is the only data
action that changes nothing and is audited anyway, because the trail is there to
answer who saw an app's data, not only who altered it.

Files are always served as `application/octet-stream` with `nosniff` and an
attachment disposition. The dashboard is served from the same origin, so an
uploaded `.html` or `.svg` rendered inline would execute as the dashboard.

## Quota

`storage.app_quota_mb` caps the combined on-disk footprint of the app's deploy
bundles (including retained versions and their runtime environments) plus its
data dir. Allow headroom for retained versions and the next deployment. The
check runs on every deployment upload and data `PUT` and is
overwrite-aware: replacing a 100 MB file with a 50 MB one always succeeds. Set
it to `0` to disable.

## Concurrent writes

The persistent dir is safe for any number of concurrent **readers**. For
concurrent **writers**, use a real database (Postgres or MySQL); local SQLite
or DuckDB in read-write mode does not survive multi-process writes.

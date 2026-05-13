# Bundle filtering

When `shinyhub deploy` builds the upload zip, it applies two independent
filtering layers: a per-tree ignore file (user-controlled, read from the
bundle root) and `bundle.Rules` (platform-enforced policy). Both the CLI
zipper and the server-side extractor call `bundle.DefaultRules()`, so
client and server enforcement cannot drift.

## Ignore files

The bundler looks for a `.shinyhubignore` file at the bundle root. If none is
found it falls back to `.gitignore`. If neither exists, no per-tree filter
is applied.

Only the file at the bundle root is read; ignore files nested inside
subdirectories are not honored. Patterns follow standard gitignore syntax:
blank lines and `#`-prefixed lines are comments, a leading `/` anchors to
the root, a trailing `/` matches directories only, and `**` matches across
path segments.

Example `.shinyhubignore`:

```gitignore
# Jupyter and editor scratch
.ipynb_checkpoints/
scratch/
*.ipynb

# Large local fixtures — push these with: shinyhub data push
fixtures/

# Re-include the seeded fixture needed at startup
!fixtures/seed.csv
```

## What the server always rejects

`bundle.DefaultRules()` defines platform-enforced policy that applies
regardless of any ignore file.

**Cache and environment directories** — silently skipped, never reported:
`.git`, `.venv`, `__pycache__`, `node_modules`, `.renv`, `.Rproj.user`.
These exist only for local tooling and have no role at runtime.

**Reserved data directories** — reported in the `Skipped from bundle` summary:

- `data/` — reserved for the platform's persistent data mount
- `datasets/` — reserved namespace for content shipped via `shinyhub data push`
- `.shinyhub-data/` — internal data namespace

Files here must be transferred with `shinyhub data push`.

**Forbidden extensions** — reported in the `Skipped from bundle` summary:
`.parquet`, `.duckdb`, `.duckdb.wal`, `.sqlite`, `.sqlite3`, `.db`, `.rds`,
`.feather`, `.arrow`, `.h5`, `.hdf5`. Transfer these via `shinyhub data push`
and read them from the app's data directory at runtime.

**Oversized files** — any single file larger than 10 MiB is rejected and
reported in the `Skipped from bundle` summary. The 128 MiB total bundle limit
is enforced separately at the multipart upload boundary.

## Precedence

The per-tree ignore file is selected in this order:

1. `.shinyhubignore` — if present at the bundle root
2. `.gitignore` — if present at the bundle root and no `.shinyhubignore` exists
3. No per-tree filter — if neither file is found

Only one file is loaded. The `bundle.Rules` filter runs after the per-tree
filter and is always active.

## Negation patterns

A line beginning with `!` re-includes a path that an earlier pattern excluded.
When any negation line is present in the ignore file, the bundler descends
into directories that would otherwise match an ignore pattern and applies
per-file matching to each entry individually. This is necessary because the
bundler cannot know at directory-traversal time whether a descendant will be
re-included.

When no negation lines are present, the bundler prunes ignored directories
entirely with `filepath.SkipDir`, which is faster for large trees.

This matches the documented limitation in Git's gitignore specification: "It
is not possible to re-include a file if a parent directory of that file is
excluded." For directory-level excludes like `cached_data/`, no negation
pattern works in practice anyway — omit `!` lines when you do not need them,
and the bundler takes the faster pruning path by default.

## Silent vs. visible exclusions

Files matched by the ignore file are filtered silently. No output is produced
for them. They represent deliberate developer intent: the operator already
knows they excluded those paths.

Files rejected by `bundle.Rules` — data directories, forbidden extensions,
oversized files — appear in a `Skipped from bundle` line printed to stderr
after the bundle is built:

```text
Skipped from bundle (push with `shinyhub data push`): reject-data-dir: data/results.csv; reject-extension: model/embeddings.parquet
```

This split is intentional. Ignore-file matches are user intent and need no
follow-up. Policy rejections indicate content that the operator must actively
move to the data directory before the app can use it at runtime.

## `shinyhub deploy --git --subdir`

When deploying a subdirectory of a cloned repository, the bundle root is set
to that subdirectory. A `.gitignore` at the repository root is not loaded.
Place a `.shinyhubignore` inside the app subdirectory if filtering is needed:

```bash
shinyhub deploy --git https://github.com/org/repo --subdir apps/dashboard
# Reads: apps/dashboard/.shinyhubignore (or apps/dashboard/.gitignore)
# Does NOT read: .gitignore at repo root
```

## Example: excluding scratch and data, keeping one file

```gitignore
# .shinyhubignore

# Scratch notebooks — not needed at runtime
scratch/
*.ipynb
.ipynb_checkpoints/

# Large local fixtures — move to data dir with shinyhub data push
fixtures/

# The seed fixture is small and required at startup
!fixtures/seed.csv
```

Because a negation line is present, the bundler descends into `scratch/`,
`fixtures/`, and any other ignored directory to apply per-file matching. The
resulting zip contains only what passes both layers:

```text
app.py
requirements.txt
www/style.css
fixtures/seed.csv
```

`fixtures/large-sample.parquet` would be excluded by the ignore pattern, but
the `.parquet` extension filter in `bundle.Rules` would reject it regardless.

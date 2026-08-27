# Bookmarking demo

This Python Shiny app exercises the selective-bookmark adapter and its native
URL restore path. It registers four different input types and uses synthetic
data only.

From the repository root:

```bash
go run ./cmd/shinyhub run examples/bookmarking-demo \
  --slug bookmarking-demo \
  --port 8800 \
  --no-reload \
  --env "PYTHONPATH=$(pwd)/packaging/python-bookmarks/src"
```

Open `http://127.0.0.1:8800/app/bookmarking-demo/`, change some filters, and
choose the bookmark icon in the ShinyHub switcher. Copy the exact view or remove
one field under **Customize…**, then open the copied URL in a new private tab.

The **Open a deliberately outdated bookmark** link exercises all recovery
paths at once: `territory` is renamed to Region, `Legacy planning` migrates to
Planning, the removed Market segment filter is ignored, and an unrecognized
setting shows its URL-provided name and saved value as bounded plain text. Open
the bookmark receipt afterwards to inspect the adjustments and copy a fresh
link.

The `PYTHONPATH` option supplies the worktree's package source after `shinyhub
run` copies the isolated app bundle. Add a released `shinyhub-bookmarks`
requirement before deploying this example as a standalone bundle.

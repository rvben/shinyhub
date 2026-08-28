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

Open `http://127.0.0.1:8800/app/bookmarking-demo/` and change some filters. The
address updates automatically, so refreshing the page or using the browser's
bookmark action restores the same view. Choose the link icon in the ShinyHub
switcher to copy the exact view or remove a field with **Change** before sharing.

The **Open a deliberately outdated bookmark** link exercises all recovery
paths at once: `territory` is renamed to Region, `Legacy planning` migrates to
Planning, the removed Market segment filter is ignored, and an unrecognized
setting shows its URL-provided name and saved value as bounded plain text. Open
the **Opened with changes** receipt afterwards to inspect the adjustments and
copy a link to the current view.

The `PYTHONPATH` option supplies the worktree's package source after `shinyhub
run` copies the isolated app bundle. The pinned package requirement is used by
standalone and public-demo deployments.

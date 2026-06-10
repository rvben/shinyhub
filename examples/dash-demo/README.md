# dash-demo

A minimal Dash app that shows how to deploy a Dash app to ShinyHub using a
manifest `[app] command`. Port and host are passed as CLI arguments so the
manifest can inject the correct values via `{port}` and `{host}` placeholders.

**Note:** `requests_pathname_prefix` in `app.py` is hardcoded to
`/app/dash-demo/` and must match the deploy slug exactly. If you deploy under
a different slug, update that line in `app.py` before deploying.

See [`docs/recipes/dash.md`](../../docs/recipes/dash.md) for the full
walkthrough including the `requests_pathname_prefix` explanation and
persistent-data usage.

## Deploy

```bash
shinyhub login --host https://your-shinyhub.example.com
shinyhub deploy examples/dash-demo --slug dash-demo
```

After deploy, the app is live at `https://your-shinyhub.example.com/app/dash-demo/`.

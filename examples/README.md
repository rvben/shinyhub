# Examples

Runnable demo bundles that you can deploy to any ShinyHub instance with a single
command. Each bundle is self-contained: `app.py`, `requirements.txt`, and
(where needed) a `shinyhub.toml` manifest.

## streamlit-demo

Shows how to deploy a Streamlit app using a manifest `[app] command`. Streamlit
does not follow the Shiny for Python bundle convention, so the manifest supplies
the full launch command with `{port}` and `{host}` placeholders. No custom
Docker image is needed. See [`docs/recipes/streamlit.md`](../docs/recipes/streamlit.md)
for the full walkthrough.

```bash
shinyhub login --host https://your-shinyhub.example.com
shinyhub deploy examples/streamlit-demo --slug streamlit-demo
```

## dash-demo

Shows how to deploy a Dash app using a manifest `[app] command`. Port and host
are passed as CLI arguments so the manifest can inject the correct values via
placeholders. The `requests_pathname_prefix` in `app.py` must match the deploy
slug; see the bundle README for details. See [`docs/recipes/dash.md`](../docs/recipes/dash.md)
for the full walkthrough.

```bash
shinyhub login --host https://your-shinyhub.example.com
shinyhub deploy examples/dash-demo --slug dash-demo
```

## identity-demo

Shows ShinyHub identity forwarding in action. The app verifies the signed
`X-Shinyhub-Identity-Token` JWT on every request and displays the caller's
username, platform role, and group memberships. An admins-only panel appears
when the verified role is `admin`. Open the app in a private window (logged
out) to see the anonymous visitor branch. See [`docs/identity.md`](../docs/identity.md)
for the full identity forwarding reference.

```bash
shinyhub login --host https://your-shinyhub.example.com
shinyhub deploy examples/identity-demo --slug identity-demo
```

# Deploy a Streamlit app

Streamlit apps do not follow the Shiny for Python bundle convention
(`app.py` with a `shiny run` entrypoint), so they require an explicit
launch command in the manifest. This recipe walks through the full
bundle layout and manifest, then shows the deploy steps.

A runnable reference bundle is in `examples/streamlit-demo/`.

## Bundle layout

```
my-streamlit-app/
  app.py
  requirements.txt
  shinyhub.toml
```

**`requirements.txt`**

```
streamlit>=1.30
```

**`shinyhub.toml`**

```toml
[app]
command = ["uv", "run", "--with-requirements", "requirements.txt",
           "streamlit", "run", "app.py",
           "--server.port", "{port}",
           "--server.address", "{host}",
           "--server.headless", "true",
           "--server.enableCORS", "false",
           "--server.enableXsrfProtection", "false"]
```

**Why `{host}` instead of a hardcoded address:**
Under the native runtime the platform expects apps to bind on
`127.0.0.1`; inside Docker containers it expects `0.0.0.0`. Hardcoding
`127.0.0.1` silently breaks Docker-mode deploys because the container
process binds to loopback and the proxy cannot reach it. The `{host}`
placeholder is substituted with the correct address for the runtime at
boot time.

**Why CORS and XSRF protection are disabled:**
ShinyHub serves all apps under a path prefix (`/app/<slug>/`). Streamlit's
built-in origin checks compare the `Origin` header against the server's own
host and reject requests that arrive via a reverse proxy under a subpath,
which causes the proxied WebSocket upgrade to be refused. Disabling these
checks delegates origin enforcement to ShinyHub (which already handles it)
and is Streamlit's own documented guidance for running behind a reverse
proxy.

**Why `--server.headless true`:**
Headless mode suppresses the "please open a browser" behaviour and the
email-capture dialog that Streamlit shows on first run. Both interfere
with automated health checks.

## Deploy

```bash
# Authenticate once (stores a token in ~/.config/shinyhub/)
shinyhub login --host https://hub.example.com

# Deploy from the bundle directory
shinyhub deploy . --slug hello-streamlit
```

For CI/CD with a pre-shared deploy token:

```bash
shinyhub deploy . --slug hello-streamlit \
  --host https://hub.example.com \
  --token "$SHINYHUB_DEPLOY_TOKEN"
```

## What to expect

1. The platform uploads and extracts the bundle.
2. Because `[app] command` is set, type detection and `uv sync` are
   skipped. Dependencies are installed by `uv run --with-requirements`
   at process start.
3. The health check polls `GET /` until Streamlit responds with a
   non-5xx status (up to 120 seconds by default).
4. The app is live at `https://hub.example.com/app/hello-streamlit/`.

View logs from the dashboard (Logs tab) or via the CLI:

```bash
shinyhub logs hello-streamlit --follow
```

## Identity forwarding

Identity headers work the same way for Streamlit apps as for Shiny
apps. The proxy injects `X-Shinyhub-User`, `X-Shinyhub-Role`, and the
signed `X-Shinyhub-Identity-Token` JWT on every authenticated request.
Read headers in Streamlit via `st.context.headers` (Streamlit 1.37+)
or the `starlette` request object. See [Identity Forwarding](../identity.md)
for the full header reference and JWT verification examples.

To opt this app out of identity forwarding:

```toml
[app]
command = [...]
identity_headers = false
```

## Persistent data

Use `{data_dir}` in the command (or the `data/` path at runtime) to
read and write files that survive redeploys:

```toml
[app]
command = ["uv", "run", "--with-requirements", "requirements.txt",
           "streamlit", "run", "app.py",
           "--server.port", "{port}",
           "--server.address", "{host}",
           "--server.headless", "true",
           "--server.enableCORS", "false",
           "--server.enableXsrfProtection", "false"]
```

Inside `app.py`, read and write persistent files under `data/`:

```python
import pathlib

DATA = pathlib.Path("data")
DATA.mkdir(exist_ok=True)

# Write
(DATA / "results.csv").write_text(df.to_csv())

# Read
df = pd.read_csv(DATA / "results.csv")
```

`data/` is a symlink provisioned by the platform that points to the
app's persistent storage volume. It is available from the first boot and
survives rollbacks.

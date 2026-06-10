# Deploy a Dash app

Dash apps do not follow the Shiny for Python bundle convention, so they
require an explicit launch command in the manifest. This recipe covers
the `requests_pathname_prefix` knob that Dash requires when served under
a path prefix, plus the full bundle layout and deploy steps.

A runnable reference bundle is in `examples/dash-demo/`.

## Bundle layout

```
my-dash-app/
  app.py
  requirements.txt
  shinyhub.toml
```

**`requirements.txt`**

```
dash>=2.16
```

**`app.py`**

Dash must be told the URL prefix it is served under, and the port and
host must come from command-line arguments so the manifest can supply
them via placeholders:

```python
import argparse
import dash
from dash import dcc, html

parser = argparse.ArgumentParser()
parser.add_argument("--port", type=int, required=True)
parser.add_argument("--host", default="127.0.0.1")
args = parser.parse_args()

app = dash.Dash(__name__, requests_pathname_prefix="/app/hello-dash/")

app.layout = html.Div([
    html.H1("Hello from Dash"),
    dcc.Graph(
        id="example",
        figure={"data": [{"x": [1, 2, 3], "y": [4, 1, 2], "type": "bar"}]},
    ),
])

if __name__ == "__main__":
    app.run(host=args.host, port=args.port)
```

**Why `requests_pathname_prefix` is required:**
ShinyHub serves every app under `/app/<slug>/`. Dash uses
`requests_pathname_prefix` to construct asset URLs and internal API
paths. Without it, Dash generates paths relative to `/` and every asset
request (CSS, JS, callbacks) returns 404 because the browser sends them
to `/assets/...` instead of `/app/hello-dash/assets/...`. The prefix
must match the deploy slug exactly.

**`shinyhub.toml`**

```toml
[app]
command = ["uv", "run", "--with-requirements", "requirements.txt",
           "python", "app.py",
           "--port", "{port}",
           "--host", "{host}"]
```

**Why `{host}` instead of a hardcoded address:**
Under the native runtime the platform expects apps to bind on
`127.0.0.1`; inside Docker containers it expects `0.0.0.0`. Hardcoding
`127.0.0.1` silently breaks Docker-mode deploys because the container
process binds to loopback and the proxy cannot reach it. The `{host}`
placeholder is substituted with the correct address for the runtime at
boot time.

## Deploy

```bash
# Authenticate once (stores a token in ~/.config/shinyhub/)
shinyhub login --host https://hub.example.com

# Deploy from the bundle directory
shinyhub deploy . --slug hello-dash
```

For CI/CD with a pre-shared deploy token:

```bash
shinyhub deploy . --slug hello-dash \
  --host https://hub.example.com \
  --token "$SHINYHUB_DEPLOY_TOKEN"
```

## What to expect

1. The platform uploads and extracts the bundle.
2. Because `[app] command` is set, type detection and `uv sync` are
   skipped. Dependencies are installed by `uv run --with-requirements`
   at process start.
3. The health check polls `GET /` until Dash responds with a non-5xx
   status (up to 120 seconds by default).
4. The app is live at `https://hub.example.com/app/hello-dash/`.

View logs from the dashboard (Logs tab) or via the CLI:

```bash
shinyhub logs hello-dash --follow
```

## Changing the slug

The `requests_pathname_prefix` in `app.py` must match the deploy slug.
If you redeploy under a different slug, update the prefix to match:

```python
app = dash.Dash(__name__, requests_pathname_prefix="/app/new-slug/")
```

Then redeploy:

```bash
shinyhub deploy . --slug new-slug
```

## Identity forwarding

Identity headers work the same way for Dash apps as for Shiny apps. The
proxy injects `X-Shinyhub-User`, `X-Shinyhub-Role`, and the signed
`X-Shinyhub-Identity-Token` JWT on every authenticated request. Read
headers in Dash via Flask's `request.headers` (Dash's underlying server
is Flask/Werkzeug):

```python
from flask import request as flask_request

@app.server.before_request
def capture_identity():
    user = flask_request.headers.get("X-Shinyhub-User")
    # ... verify the JWT from X-Shinyhub-Identity-Token for security decisions
```

See [Identity Forwarding](../identity.md) for the full header reference
and JWT verification examples.

To opt this app out of identity forwarding:

```toml
[app]
command = [...]
identity_headers = false
```

## Persistent data

Use the `data/` path to read and write files that survive redeploys.
The platform provisions `data/` as a symlink to the app's persistent
storage volume, available from the first boot:

```python
import pathlib

DATA = pathlib.Path("data")
DATA.mkdir(exist_ok=True)

# Write
(DATA / "cache.json").write_text(json.dumps(my_data))

# Read
my_data = json.loads((DATA / "cache.json").read_text())
```

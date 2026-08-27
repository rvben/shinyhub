---
description: "Try the real ShinyHub server, develop an app with safe reloads, and deploy it through the same production workflow."
---

# Quick start

In about five minutes, you will run the real ShinyHub server, develop a Python
Shiny app with safe reloads, and deploy that app to your local hub.

## Before you begin

Install [`uv`](https://docs.astral.sh/uv/getting-started/installation/). It runs
ShinyHub without modifying your project and is also the runtime ShinyHub uses
for Python applications. No ShinyHub account or external service is required.

## Terminal 1: run ShinyHub

```bash
uvx shinyhub serve
```

On the first run, ShinyHub asks you to create a local administrator, then:

- writes a private, loopback-only `shinyhub.yaml`;
- creates a persistent SQLite database under `./data/`;
- opens `http://127.0.0.1:8080` in your browser; and
- deploys **ShinyHub Tour**, a real Python Shiny app, through the normal build
  and runtime pipeline.

Sign in with the administrator you created. Leave this terminal running while
you work through the next steps. The Tour is an ordinary private app, so its
deployment, logs, settings, and files behave exactly like your own apps.

Existing installations, explicit `--config` launches, unattended setups,
PostgreSQL servers, and non-loopback servers are never populated with example
content. Add `--no-browser` when you do not want ShinyHub to open a browser.

## Terminal 2: develop an app

Create a small app in a second terminal:

```bash
mkdir hello-shinyhub
cd hello-shinyhub
```

Create `app.py`:

```python
from shiny import App, ui

app_ui = ui.page_fluid(
    ui.h2("Hello from ShinyHub"),
    ui.p("A production-shaped app with a safe local loop."),
)

app = App(app_ui, server=None)
```

Create `requirements.txt`:

```text
shiny>=1.6
```

Now start the development loop:

```bash
uvx shinyhub dev . --open
```

ShinyHub installs the app dependencies, opens its production-shaped
`/app/hello-shinyhub/` route, and watches the directory. Change the heading in
`app.py` and save: the candidate starts separately and must become healthy
before the browser switches to it. A syntax error leaves the last healthy
version running, and generated environments never enter your source directory.

This command does not contact the server in terminal 1. Local development is
the safe default; moving the loop to a host always requires an explicit
`--remote <host>`.

When you are ready to deploy, stop `dev` with <kbd>Ctrl</kbd>+<kbd>C</kbd>.

## Deploy to your local hub

From the same app directory:

```bash
uvx shinyhub connect http://localhost:8080 --name local
uvx shinyhub doctor .
uvx shinyhub plan .
uvx shinyhub deploy . --open
```

`connect` saves a credential for the local hub. `doctor` reports every blocker
without changing state, and `plan` shows the exact bundle, configuration, and
lifecycle effects before `deploy` changes the server. The final command waits
for health and opens the deployed app.

For everyday use, install the same CLI once and drop the `uvx` prefix:

```bash
uv tool install shinyhub
shinyhub dev .
```

## Choose your next step

- Learn the complete [application development workflow](../local-development.md).
- Run every local app in a [fleet checkout](../development/fleet.md).
- Move the same loop to an explicit [remote development host](../development/remote.md).
- Add names, limits, scaling, and lifecycle settings in the
  [application manifest](../manifest.md).
- Follow an [R Shiny](../recipes/r-shiny.md),
  [Dash](../recipes/dash.md), or [Streamlit](../recipes/streamlit.md) recipe.

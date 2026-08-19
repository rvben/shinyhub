---
description: "Go from nothing installed to a live Shiny app, then deploy one of your own through the same workflow you would use in production."
---

# Quick start

Go from nothing installed to a live Shiny application, then deploy one of your
own through the same production workflow.

## Start a real local server

```bash
uvx shinyhub serve
```

That is the whole first run. ShinyHub asks you to create a local administrator,
then:

- writes a private, loopback-only `shinyhub.yaml`;
- creates a persistent SQLite database under `./data/`;
- opens `http://127.0.0.1:8080` in your browser; and
- deploys **ShinyHub Tour**, a reactive Python Shiny app with bundled example
  data, through the real build and runtime pipeline.

Sign in with the administrator you just created. The example is an ordinary,
private app—not a special demo screen—so its deployment, logs, settings, and
files behave exactly like your own applications. Stop the server with
<kbd>Ctrl</kbd>+<kbd>C</kbd>; run the same command in the same directory to
continue with the state you already created.

Use `uvx shinyhub serve --no-browser` when you do not want ShinyHub to launch a
browser. Existing installations, explicit `--config` launches, unattended
setups, PostgreSQL servers, and non-loopback servers are never populated with
example content.

## Deploy your own app

```bash
mkdir hello-shinyhub
cd hello-shinyhub
```

Create `app.py`:

```python
from shiny import App, ui

app_ui = ui.page_fluid(
    ui.h2("Hello from ShinyHub"),
    ui.p("A production-shaped deployment in a few commands."),
)

app = App(app_ui, server=None)
```

Create `requirements.txt`:

```text
shiny>=1.6
```

## Validate locally

```bash
shinyhub doctor . --local
shinyhub run . --check
```

`doctor` reports all bundle and runtime problems in one pass. `run --check`
boots the app through the production-shaped `/app/<slug>/` route and exits after
readiness succeeds.

## Connect and deploy

```bash
shinyhub connect http://localhost:8080 --name local
shinyhub doctor .
shinyhub plan . --slug hello
shinyhub deploy . --slug hello --open
```

`plan` is read-only. It shows the exact archive, inferred launch command,
manifest changes, permissions, and start/stop effects before `deploy` changes
the server.

For regular use, install the command once with `uv tool install shinyhub`. The
one-shot `uvx` command and the installed command run the same server binary.

## Continue

- Add names, limits, scaling, and lifecycle settings in the
  [application manifest](../manifest.md).
- Configure [environment variables and encrypted secrets](../environment.md).
- Move persistent files into the [application data directory](../data.md).
- Use a [fleet manifest](../fleet.md) to reconcile several applications.

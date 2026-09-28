# Agent tools in a real Shiny app

This example uses server-owned reactive state. The allowlisted
`set_dashboard_period` handler changes only the current viewer's session and
returns the applied period and metrics. The same registry serves browser
WebMCP and the built-in chat. Without an API key, the browser-tool example
still works and the chat panel stays hidden.

To run from this repository:

```bash
UV_CACHE_DIR=/tmp/shinyhub-uv-cache uv run --no-project \
  --with ./packaging/python-agent \
  --with shiny \
  shiny run examples/agent-shiny-demo/app.py
```

For a live chat, set `OPENAI_API_KEY` as a private app environment secret. A
hoster can instead set `SHINYHUB_AGENT_AGUI_URL` to an HTTPS AG-UI endpoint and
optionally set `SHINYHUB_AGENT_AGUI_TOKEN` as a private app secret. The AG-UI
endpoint takes precedence. Credentials stay in the app process. Each viewer
has an independent conversation and approval flow. The agent can read metrics
and propose a period change; a visitor must choose **Apply change** before the
Shiny server executes it.

The `shinyhub-agent` package is currently local to this repository. To deploy
this example, build its wheel, place the wheel in the app bundle, add that wheel
to `requirements.txt`, and then deploy. Do not publish the package or deploy
this example until its release and integration gates are reviewed.

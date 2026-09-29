# Agent tools in a real Shiny app

This example uses server-owned reactive state. The allowlisted
`set_dashboard_period` handler changes only the current viewer's session and
returns the applied period and metrics. The same registry serves browser
WebMCP and the built-in chat. Without an API key, the browser-tool example
still works and the chat panel stays hidden.

To run from this repository:

```bash
uv run --no-project --with 'shinyhub-agent[bedrock]==0.2.0b1' \
  shiny run examples/agent-shiny-demo/app.py
```

For a live chat, set `OPENAI_API_KEY` as a private app environment secret. A
hoster can instead set `SHINYHUB_AGENT_BEDROCK_MODEL_ID` and `AWS_REGION` and
install `shinyhub-agent[bedrock]`. The app's AWS identity needs
`bedrock:InvokeModelWithResponseStream` for that model or inference profile.
Alternatively set `SHINYHUB_AGENT_AGUI_URL` to an HTTPS AG-UI endpoint and
optionally set `SHINYHUB_AGENT_AGUI_TOKEN` as a private app secret. AG-UI takes
precedence, then Bedrock, then OpenAI. Credentials stay in the app process.
Each viewer
has an independent conversation and approval flow. The agent can read metrics
and propose a period change; a visitor must choose **Apply change** before the
Shiny server executes it.

The app's `requirements.txt` pins the published package. Set the desired chat
secret in ShinyHub's app environment settings, then deploy this directory with
`shinyhub deploy examples/agent-shiny-demo --slug agent-shiny-demo`.

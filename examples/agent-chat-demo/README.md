# Agent capabilities demo

A self-contained prototype showing two independent capabilities for a ShinyHub
app. The hoster can provide a chat agent in a native ShinyHub overlay, and the app
can expose browser tools for a visitor's own agent. The dashboard values are
synthetic. ShinyHub's **Ask** toolbar control opens the overlay over the full-width
dashboard; it fills the screen on narrow viewports.
Outside ShinyHub, the app shows its own **Ask the assistant** launcher.
With an OpenAI API key, the app runs a tool-using agent on the
server. A hoster can instead provide an AG-UI endpoint. With neither configured,
chat replies are scripted and labeled **Demo agent**.

```bash
shinyhub run examples/agent-chat-demo --open
```

The hoster can enable either capability without enabling the other. Set these
as per-app environment variables in ShinyHub before starting the app:

| Variable | Purpose |
| --- | --- |
| `AGENT_DEMO_AGUI_URL` | Full URL of the hoster's AG-UI run endpoint. |
| `AGENT_DEMO_AGUI_TOKEN` | Optional bearer token, stored as a ShinyHub app secret. |
| `OPENAI_API_KEY` | OpenAI API key, stored as a ShinyHub app secret; enables the built-in agent when no AG-UI URL is set. |
| `AGENT_DEMO_OPENAI_MODEL` | Model for the built-in agent; defaults to `gpt-4.1-mini`. |
| `AGENT_DEMO_ENABLE_CHAT` | `true` by default; set `false` to hide the assistant and disable `/chat`. |
| `AGENT_DEMO_ENABLE_WEBMCP` | `true` by default; set `false` to skip browser tool registration. |

The browser calls only this app's `/chat` route. In OpenAI mode, the app sends
the typed question and recent chat history to the Responses API. It sets
`store: false` and offers two read-only functions, `get_dashboard_state` and
`compare_dashboard_periods`, plus `set_dashboard_period`. The latter accepts
only the three available views and sends a validated change event to the page.
The app executes these functions against the same `dashboard.json` values
rendered in the browser. The key stays on the server. The agent can describe
values and change the visible period, but cannot change underlying data or
access ShinyHub identity or other apps. All values, including This year, are
illustrative.

OpenAI replies use Responses API streaming. The chat shows progress only for
observable steps (request sent, dashboard tool selected, answer being written),
then displays text as it arrives. A period change updates the chart and context
label, and adds an action receipt with **Undo**. **Stop** cancels the browser
request; an interrupted answer is labeled and excluded from later model
context. A failed request offers **Retry**. The UI does not expose or invent
the model's private reasoning. AG-UI agents can send their own text and tool
lifecycle events through the same panel.

If `AGENT_DEMO_AGUI_URL` is set, it takes precedence over `OPENAI_API_KEY`. The
app sends a standard AG-UI `RunAgentInput` to that endpoint and projects text
events into the chat UI. The bearer token remains on the server. This path
includes the question, up to 20 previous messages, and selected period. It
does not include ShinyHub cookies, identity headers, app data, or tools. This
prototype intentionally ignores remote agent tool calls.

When WebMCP is enabled, this page registers two real browser tools using
`document.modelContext.registerTool`:

| Tool | Effect |
| --- | --- |
| `get_dashboard_state` | Read the selected period and synthetic dashboard values. |
| `set_dashboard_period` | Change the selected period between This week, Last week, and This year. |

These tools run in the visitor's current page. They are available only when the
browser supports WebMCP and an agent client can discover them. The **Try changing
the period** button exercises the same app function locally, so the interaction
can be previewed in other browsers; it does not simulate an external agent
connection. WebMCP is a draft browser API and is currently available in Chrome
through an origin trial or a local development flag. For an external agent that
does not share the visitor's browser session, this demo does not provide a
remote MCP server or permission delegation.

This example demonstrates two app-level capabilities; it does not add a
platform-wide agent registry, app context API, conversation storage, remote
MCP server, or permission delegation to ShinyHub.

To deploy after review:

```bash
shinyhub deploy examples/agent-chat-demo --slug agent-chat-demo --open
```

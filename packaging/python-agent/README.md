# ShinyHub agent tools for Python Shiny

This helper lets an app declare a small, typed set of tools for the **current
viewer session**. The app owns all data access and state changes. ShinyHub does
not infer tools from visible inputs or let a model run arbitrary R/Python code.
The same registry powers built-in chat and browser WebMCP.

```python
from shiny import App, reactive, ui
from shinyhub_agent import AgentTool, agent_dependency, register

app_ui = ui.page_fluid(agent_dependency(), ui.input_select("period", "Period", ["week", "year"]))

def server(input, output, session):
    selected_period = reactive.value("week")

    @reactive.effect
    @reactive.event(input.period)
    def from_control():
        selected_period.set(input.period())

    async def current_view(args):
        return {"period": selected_period.get()}

    async def set_period(args):
        selected_period.set(args["period"])
        ui.update_select("period", selected=args["period"], session=session)
        return {"period": selected_period.get()}

    register(session=session, input=input, tools=[
        AgentTool("get_view", "Read the selected period", {
            "type": "object", "properties": {}, "additionalProperties": False,
        }, current_view),
        AgentTool("set_period", "Change the selected period", {
            "type": "object", "properties": {"period": {"type": "string", "enum": ["week", "year"]}},
            "required": ["period"], "additionalProperties": False,
        }, set_period, read_only=False, confirmation="Change the dashboard period?"),
    ])

app = App(app_ui, server)
```

The helper sends version 1 capability messages over the existing Shiny session.
Arguments are validated again on the server with JSON Schema. Each session has
its own registry, nonce, concurrency lock, request budget, timeouts, and bounded
messages. Errors do not reveal handler exceptions or tool arguments. A handler
must perform its own permission checks for sensitive data or actions.

## Built-in chat or a hoster-owned agent

Add `chat_dependency()` to the UI and pass one chat backend to `register()`:

On ShinyHub versions with toolbar chat support, the helper announces chat
availability and the toolbar shows **Ask**. Its own launcher remains available
outside ShinyHub or when the toolbar is hidden. Apps without chat do not show
**Ask**. The app still owns its chat panel and agent backend.

```python
import os
from shinyhub_agent import AGUIChat, OpenAIChat, chat_dependency

# Include chat_dependency() alongside agent_dependency() in the app UI.
if os.environ.get("SHINYHUB_AGENT_AGUI_URL"):
    chat = AGUIChat(
        endpoint=os.environ["SHINYHUB_AGENT_AGUI_URL"],
        bearer_token=os.environ.get("SHINYHUB_AGENT_AGUI_TOKEN", ""),
    )
else:
    chat = OpenAIChat(
        api_key=os.environ["OPENAI_API_KEY"],
        instructions="You help with this dashboard. Use registered tools for app facts.",
    )
register(session=session, input=input, tools=tools, chat=chat)
```

The app stores both credentials as private ShinyHub environment secrets. The
browser never receives them. OpenAI requests use the Responses API with
streaming, bounded output, and `store: false`. AG-UI requests carry the current
session's thread ID, recent messages, and registered tool schemas; they do not
carry ShinyHub cookies, identity headers, or other apps' data. The hoster must
authorize and secure their endpoint. Tool calls from either backend are
validated again by the app. A write pauses for visitor approval in the chat
panel, then runs the handler and returns its applied result to the agent.

When WebMCP is available, `bridge.js` registers the declared tools. In other
browsers it makes them available through `window.shinyhubAgentTools.invoke()`
for an app-supplied assistant. Browser tool writes show visitor confirmation
before dispatch. This confirmation is a browser interaction, **not a security
authorization boundary**; the app handler still decides what the viewer may do.

The chat history exists only in the viewer's Shiny session and is limited to
the last six exchanges. A new chat clears it. The app currently has no durable
conversation store. The helper does not provide a remote MCP server or a
platform-wide agent registry, administration UI, or billing controls.

## Operational requirements

- Keep app access behind ShinyHub's authentication and per-app access policy.
- Give tools the least authority required, and check the viewer's permissions
  inside handlers for sensitive reads and actions.
- Return only data the viewer may see. Tool results are sent to the model
  provider or hoster-owned AG-UI endpoint to compose an answer.
- Set model and endpoint secrets per app, never in the page or manifest.
- Review tool names, schemas, descriptions, and app instructions when the app
  changes. Add a regression test for each consequential action.
- Run at most one chat request and one tool request at a time per viewer;
  the adapter enforces per-session request budgets and bounded payloads.

The package is a reusable app integration. A platform-owned chat service,
central cost policy, admin configuration, and R Shiny helper remain separate
work before this becomes a platform-wide production feature.

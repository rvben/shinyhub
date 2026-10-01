# ShinyHub agent tools for Python Shiny

**Experimental.** This prerelease helper is for evaluation. Its APIs and
behavior may change between releases. Do not depend on it for critical
workflows; review agent results and keep a manual path for consequential
actions.

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

    # Explicitly allow browser agents to change this session's display filter.
    register(session=session, input=input, allow_browser_writes=True, tools=[
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
availability and the toolbar shows **Ask**. On hosts with native chat support,
ShinyHub supplies the overlay frame and controls while the helper supplies the
conversation body and agent backend. Its own launcher and frame remain available
outside ShinyHub or when the toolbar is hidden. Apps without chat do not show
**Ask**.

Assistant answers render basic Markdown in both panel layouts: paragraphs,
line breaks, `**bold**`, `*italic*`, inline code, bullet and numbered lists
with one nested level, fenced code blocks, and simple pipe tables. ATX headings
(`# Heading`) appear as bold paragraphs. **Copy answer** copies the original
Markdown, including its tables. Questions remain plain text.

Tables require a header and separator with matching column counts; each
separator cell contains at least three hyphens. Outer pipes are optional.
Escape a pipe within a cell as `\|`, including inside inline code. Use `---:`
to right-align a column or `:---:` to center it. Body rows must match the header
width; malformed rows remain visible as text rather than losing values. Wide
tables and code blocks scroll horizontally inside the answer.

On desktop, drag the panel's left edge or choose **Expand assistant** for more
room; **Restore assistant width** returns to your previous width. The resize
edge also supports Left/Right arrows (Shift for larger steps) and Home/End.
Width and expansion are remembered for the tab, when session storage is
available, and constrained to the viewport. Small screens keep the full-screen
layout. Descriptive table cells wrap; numeric values stay together. Native
panel resizing requires a ShinyHub toolbar that supports these controls.

Links, autolinks, images and HTML never create active browser content. Their
syntax stays visible, as do unsupported markers such as blockquotes, task lists,
footnotes, underscore emphasis and strikethrough. Inline emphasis uses asterisks;
backslash escapes preserve literal punctuation. Every source line break is
displayed. This is a limited dialect, not full CommonMark or GFM support.

`OpenAIChat` and `BedrockChat` append a shared description of these formatting
capabilities to the app's `instructions` on every model request, including tool
follow-ups. Explicit app formatting preferences still take precedence. With
`AGUIChat`, configure the same formatting guidance on the hoster-owned endpoint;
the helper does not modify that agent's system prompt. Formatting guidance is
advisory; safe rendering does not depend on the model following it.

```python
import os
from shinyhub_agent import AGUIChat, BedrockChat, OpenAIChat, chat_dependency

# Include chat_dependency() alongside agent_dependency() in the app UI.
if os.environ.get("SHINYHUB_AGENT_AGUI_URL"):
    chat = AGUIChat(
        endpoint=os.environ["SHINYHUB_AGENT_AGUI_URL"],
        bearer_token=os.environ.get("SHINYHUB_AGENT_AGUI_TOKEN", ""),
    )
elif os.environ.get("SHINYHUB_AGENT_BEDROCK_MODEL_ID"):
    chat = BedrockChat(
        model_id=os.environ["SHINYHUB_AGENT_BEDROCK_MODEL_ID"],
        region=os.environ["AWS_REGION"],
        instructions="You help with this dashboard. Use registered tools for app facts.",
    )
else:
    chat = OpenAIChat(
        api_key=os.environ["OPENAI_API_KEY"],
        instructions="You help with this dashboard. Use registered tools for app facts.",
    )
register(session=session, input=input, tools=tools, chat=chat)
```

Install `shinyhub-agent[bedrock]` for `BedrockChat`. It uses Bedrock's
`ConverseStream` API and the standard AWS credential chain. Give the app only
`bedrock:InvokeModelWithResponseStream` for its chosen model or inference
profile. The model must support streaming tool use. On an on-premises ShinyHub
host, store AWS
credentials as private per-app secrets; on AWS, prefer a scoped workload role.
Choose a model ID available in the selected region. The adapter does not infer
one because model and tool support vary by region. Bedrock requests inherit the
AWS account's invocation logging and data policies.

The browser never receives model or endpoint credentials. OpenAI requests use
the Responses API with streaming, bounded output, and `store: false`. AG-UI
requests carry the current session's thread ID, recent messages, and registered
tool schemas; they do not carry ShinyHub cookies, identity headers, or other
apps' data. The hoster must
authorize and secure their endpoint. Tool calls from all three backends are
validated again by the app. A write pauses for visitor approval in the chat
panel, then runs the handler and returns its applied result to the agent.
For a clear action result, a write tool may provide `receipt=lambda args,
result: f"View set to {result['period']}"`. A tool may also provide an async
`undo(args, result)` handler. The chat then offers Undo for five minutes. Undo
runs in the same viewer's Shiny session without asking the model; the handler
must verify the app is still in the state created by that action before
restoring the previous state. Only the most recent write remains undoable.

An AG-UI agent hosted in Amazon Bedrock AgentCore needs an authenticated
`InvokeAgentRuntime` client or a hoster-managed HTTPS relay. `AGUIChat` does
not sign AgentCore requests itself.

When WebMCP is available, `bridge.js` registers the exposed tools. In other
browsers it makes them available through `window.shinyhubAgentTools.invoke()`
for an app-supplied assistant. By default, browser capabilities contain only
read-only tools, and the server rejects direct calls to write tools even with
a valid session nonce. Chat retains the full registry and server-side approval
for writes. To expose writes to browser agents, explicitly pass
`allow_browser_writes=True` to `register()` (as in the display-filter example).
Opted-in browser writes show visitor confirmation before dispatch, but that
confirmation can be bypassed by a client. It is **not a security authorization
boundary**; the app handler must still decide what the viewer may do. Keep
consequential actions on the chat path when server-side approval is required.

Upgrading from a release that exposed browser writes automatically requires
this explicit opt-in to retain that behavior. Chat-only integrations need no
change.

## Data sent to agents

Tool results are model input. Return the same authorized and anonymised data
as the page, reduced to the fields needed for the question. Resolve identities
such as "me" from verified session identity in the handler, and apply access
filters before aggregation. Do not accept a caller-supplied user or project as
the authority for scope. Avoid returning identity tokens, credentials, or
unnecessary names and identifiers, even if the UI can display them.

Build tools and schemas inside the top-level server function for each viewer.
Enums can be computed from that viewer's authorized values; schemas and
descriptions are also visible to agents. An enum is a schema constraint, not
an authorization check: the handler must re-check access on every call,
including after permissions change. Test ordinary, manager, admin, and
anonymised views against the page's access rules, including requests for
another viewer's data. A result-size limit does not redact sensitive fields.

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

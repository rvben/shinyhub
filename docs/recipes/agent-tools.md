---
description: "Give a Python Shiny app a session-scoped assistant and WebMCP tools that call explicit, validated actions on each viewer's own session."
---

# Agent tools in Python Shiny

`shinyhub-agent` is available as a regular `0.2.0` release. Its API is still
below `1.0`; pin the package and review migration notes before upgrading.
Browser WebMCP availability depends on the browser. Review agent results and
keep a manual way to complete consequential actions.

The `shinyhub-agent` helper lets an app author register a small set of typed
tools for each viewer's Shiny session. The same tools are available to a chat
agent and, where supported by the browser, a visitor's WebMCP agent. The app
keeps control of what data can be read and what state can change.

Install the [published `shinyhub-agent` package](https://pypi.org/project/shinyhub-agent/)
in a Python Shiny app:

```text
shinyhub-agent==0.2.0
```

The repository contains a [runnable example](https://github.com/rvben/shinyhub/tree/main/examples/agent-shiny-demo)
and the [helper API and security notes](https://github.com/rvben/shinyhub/tree/main/packaging/python-agent).

## Add tools to an app

Include `agent_dependency()` in the UI. In the top-level server function,
register tools once per viewer session. Each handler is an `async` function
that receives validated JSON arguments and returns a bounded JSON result.

```python
from shiny import App, reactive, ui
from shinyhub_agent import AgentTool, agent_dependency, register

app_ui = ui.page_fluid(
    agent_dependency(),
    ui.input_select("period", "Period", ["week", "year"]),
)

def server(input, output, session):
    period = reactive.value("week")

    @reactive.effect
    @reactive.event(input.period)
    def manual_change():
        period.set(input.period())

    async def get_view(_args):
        return {"period": period.get()}

    async def set_period(args):
        period.set(args["period"])
        ui.update_select("period", selected=args["period"])
        return {"period": period.get()}

    # Allow browser agents to change only this viewer's display filter.
    register(session=session, input=input, allow_browser_writes=True, tools=[
        AgentTool("get_view", "Read the selected period", {
            "type": "object", "properties": {}, "additionalProperties": False,
        }, get_view),
        AgentTool("set_period", "Change the selected period", {
            "type": "object",
            "properties": {"period": {"type": "string", "enum": ["week", "year"]}},
            "required": ["period"], "additionalProperties": False,
        }, set_period, read_only=False,
            confirmation="Change this dashboard's reporting period?",
            describe=lambda args: f"Reporting period = {args['period']}"),
    ])

app = App(app_ui, server)
```

The helper validates names and schemas at startup. It validates arguments
again before calling a handler, binds requests to the current Shiny session,
limits request volume and payload size, and returns safe errors. A write is
reported as applied only after its handler returns. For sensitive tools, check
the viewer's authorization in the handler as well.

Registered callbacks run with the viewer's session context, isolated reactive
reads and the reactive graph lock; pending work is flushed afterwards. The
example needs no explicit `session=`, `reactive.isolate()` or manual flush.
Keep handlers short. Input updates are client messages, so return normalized
server-owned state as above rather than reading `input.period()` immediately
after updating it. Use the same normalization for manual changes and tools.

For dynamic constraints, add `validate(args)` to a tool. It must return `None`
or raise a visitor-safe `ToolError(code, message)` and may be synchronous or
async. It runs before approval and again before execution, without changing
arguments. Optional synchronous `describe(args)` supplies up to 300 characters
of plain text for approval. Complete nested arguments remain inspectable.

## Add chat

Include `chat_dependency()` in the UI and pass a chat backend to `register()`.
The same package provides all three backends; choose one in your app's
configuration.

`OpenAIChat` uses a private `OPENAI_API_KEY` app secret and app-specific
instructions. `BedrockChat` uses AWS credentials and a selected Bedrock model
through `ConverseStream`. `AGUIChat` connects to a hoster-owned HTTPS AG-UI
endpoint and can use a private bearer token. All three backends use the same
app tool registry. The browser receives no backend credentials.
When this app runs on a ShinyHub server with toolbar chat support, **Ask**
appears in the app toolbar after the chat session connects. The helper uses its
own launcher if the toolbar is unavailable or hidden. The app remains
responsible for its panel, model instructions, and tools.
It also decides what visitors need to know about the assistant's capabilities,
limits, and how to check consequential results. These decisions belong to the
app author; ShinyHub does not apply an assistant warning to every hosted app.

```python
import os
from shinyhub_agent import OpenAIChat, chat_dependency

chat = OpenAIChat(
    api_key=os.environ["OPENAI_API_KEY"],
    instructions="Use the registered app tools for facts about the current view.",
)

register(session=session, input=input, tools=tools, chat=chat)
```

For Bedrock, install `shinyhub-agent[bedrock]==0.2.0` instead of the base
requirement. This installs `boto3` for the same package and version. Then supply
an AWS region and a model ID that supports streaming tool use:

```python
from shinyhub_agent import BedrockChat

chat = BedrockChat(
    model_id=os.environ["SHINYHUB_AGENT_BEDROCK_MODEL_ID"],
    region=os.environ["AWS_REGION"],
    instructions="Use the registered app tools for facts about the current view.",
)
```

The app's AWS identity needs `bedrock:InvokeModelWithResponseStream` for the
selected model or inference profile. Use per-app private credentials on an
on-premises host or a scoped workload role on AWS. Bedrock access and model
availability depend on the chosen account and region.

To bring your own agent, construct `AGUIChat` in place of `OpenAIChat`:

```python
import os
from shinyhub_agent import AGUIChat

chat = AGUIChat(
    endpoint=os.environ["SHINYHUB_AGENT_AGUI_URL"],
    bearer_token=os.environ.get("SHINYHUB_AGENT_AGUI_TOKEN", ""),
)

register(session=session, input=input, tools=tools, chat=chat)
```

Chat shows observed progress, streams answer text, and pauses before a write.
The visitor sees the proposed action and chooses whether to apply it. The
server executes an approved handler and returns the applied result to the
agent. Conversation history stays in the Shiny session and is bounded; it is
not stored durably by this helper.

All backends default to two calls and one write per step, eight calls per turn
and four tool rounds. Excess calls receive correlated deferred results; later
calls in that step are also deferred so reads cannot pass a pending write.
When the budget runs out, one additional tools-disabled call answers from
completed results, with a deterministic fallback if that call fails.
Configure `max_tool_calls_per_step` (1–8), `max_tool_calls_per_turn` (1–64) and
`max_tool_rounds` (1–8) on the backend. OpenAI and Bedrock accept
`max_output_tokens` (100–8192; default 500).

In `register()`, configure `approval_timeout` (1–300 seconds; default 30),
`history_exchanges` (1–32; default 6) and `max_answer_chars` (1000–32768;
default 8000). Cards show deadlines and expiration. Arguments and schemas
are capped at 8192 UTF-8 bytes, results at 32768 bytes, and tool execution at
8 seconds. The helper README lists all fixed limits.

Set `on_usage(record)` on the backend for a record per provider request:
timestamp, model, turn/call IDs, round, tokens, duration, requested tools and
outcome. Records also appear in structured INFO logs and model-call span
attributes when tracing is available. Use `usage_metadata={"username": ...}`
in `register()` for optional attribution from verified identity; it is not
sent in prompts or HTTP headers. AG-UI reports endpoint duration with unknown
token counts; model accounting belongs on its external endpoint.

Browser tests can open either panel with
`await page.waitForFunction(() => window.shinyhubAgentChat?.open())` and use
`.sh-agent-input` and `.sh-agent-send`. This public hook is idempotent and
works without opening or patching the toolbar's shadow root.

Assistant answers support basic Markdown in the native toolbar overlay and
fallback panel: paragraphs, line breaks, `**bold**`, `*italic*`, inline code,
bullet and numbered lists with one nested level, fenced code blocks, and simple
pipe tables. ATX headings appear as bold paragraphs. Questions remain plain
text, and **Copy answer** copies the original Markdown source.

For tables, provide a header and separator with the same number of cells, using
at least three hyphens per separator cell. Use `---:` for numeric columns and
`:---:` for centered columns; escape a pipe within a cell as `\|`. Each body row
must match the header width. Malformed rows remain visible as text so values are
never silently dropped. Wide tables and code blocks scroll within the message.
Descriptive cells wrap while numeric values stay together. On desktop, use
**Expand assistant** or drag the panel's left edge to view more columns.
**Restore assistant width** returns to the previous width. The resize edge
supports arrow keys and Home/End, and the tab remembers your preferred size.
Small screens keep the full-screen layout. Native resizing also requires
support in the ShinyHub toolbar.

Links, images, autolinks and raw HTML stay visible without creating active
browser content. Blockquotes, task lists, footnotes, underscore emphasis and
strikethrough are unsupported; their markers stay visible. The panel preserves
source line breaks and supports backslash escapes for literal punctuation.
It implements a limited dialect rather than full CommonMark or GFM.

`OpenAIChat` and `BedrockChat` append a shared formatting-capability description
to your `instructions` on every request. Keep app-specific behavior and tool
guidance in `instructions`; explicit formatting preferences take precedence.
For `AGUIChat`, configure this formatting contract on your external agent.
The renderer remains safe regardless of whether the model follows the guidance.

## Browser agents

Where `document.modelContext.registerTool` exists, the helper registers the
exposed tools as WebMCP browser tools. This path uses the visitor's current
Shiny session. Other browsers continue to run the app normally; they can use
the built-in chat if configured.

Browser tools are read-only by default. The server rejects write calls even
when a client bypasses discovery and supplies a valid session nonce. The
example explicitly enables browser writes with `allow_browser_writes=True`
for a per-viewer display filter. This opt-in permits direct browser writes;
the browser confirmation is not server-side approval. Keep actions requiring
server-side approval on the chat path, which retains write tools and its
approval flow regardless of this setting. Earlier releases exposed browser
writes automatically; retaining that behavior now requires the explicit opt-in.

## Data access and minimisation

Tool results go to the model provider or the hoster-owned agent endpoint.
Reuse the page's access filters and anonymisation before returning results;
return only the fields needed to answer the question. Resolve "me" from the
verified session identity, never from caller-supplied identity arguments.

Construct each viewer's tools in their server session. Schema enums and tool
descriptions must contain only values that viewer may see. Enums are not
authorization checks: handlers must re-check current permissions on each
call. Add role-based regression checks for unauthorized slices and for views
where the page hides user identifiers. See the helper's
[security notes](https://github.com/rvben/shinyhub/tree/main/packaging/python-agent#data-sent-to-agents).

This integration covers Python Shiny apps. It does not yet provide a platform
administration screen, central model budget, durable conversation store,
remote MCP server, or R Shiny helper.

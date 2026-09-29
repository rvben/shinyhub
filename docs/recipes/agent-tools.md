---
description: "Give a Python Shiny app a session-scoped assistant and WebMCP tools that call explicit, validated actions on each viewer's own session."
---

# Agent tools in Python Shiny

**Experimental.** The chat, WebMCP integration, and `shinyhub-agent` helper are
available for evaluation. Their APIs and behavior may change between releases.
Do not depend on them for critical workflows; review agent results and keep a
manual way to complete consequential actions.

The `shinyhub-agent` helper lets an app author register a small set of typed
tools for each viewer's Shiny session. The same tools are available to a chat
agent and, where supported by the browser, a visitor's WebMCP agent. The app
keeps control of what data can be read and what state can change.

Install the [published `shinyhub-agent` package](https://pypi.org/project/shinyhub-agent/)
in a Python Shiny app:

```text
shinyhub-agent==0.2.0b2
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
        ui.update_select("period", selected=args["period"], session=session)
        return {"period": period.get()}

    register(session=session, input=input, tools=[
        AgentTool("get_view", "Read the selected period", {
            "type": "object", "properties": {}, "additionalProperties": False,
        }, get_view),
        AgentTool("set_period", "Change the selected period", {
            "type": "object",
            "properties": {"period": {"type": "string", "enum": ["week", "year"]}},
            "required": ["period"], "additionalProperties": False,
        }, set_period, read_only=False,
            confirmation="Change this dashboard's reporting period?"),
    ])

app = App(app_ui, server)
```

The helper validates names and schemas at startup. It validates arguments
again before calling a handler, binds requests to the current Shiny session,
limits request volume and payload size, and returns safe errors. A write is
reported as applied only after its handler returns. For sensitive tools, check
the viewer's authorization in the handler as well.

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
limits, and how to check consequential results. ShinyHub's experimental status
is guidance for app authors, not a label applied to every hosted app.

```python
import os
from shinyhub_agent import OpenAIChat, chat_dependency

chat = OpenAIChat(
    api_key=os.environ["OPENAI_API_KEY"],
    instructions="Use the registered app tools for facts about the current view.",
)

register(session=session, input=input, tools=tools, chat=chat)
```

For Bedrock, install `shinyhub-agent[bedrock]==0.2.0b2` instead of the base
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

## Browser agents

Where `document.modelContext.registerTool` exists, the helper registers each
declared tool as a WebMCP browser tool. This path uses the visitor's current
Shiny session. Other browsers continue to run the app normally; they can use
the built-in chat if configured.

This integration covers Python Shiny apps. It does not yet provide a platform
administration screen, central model budget, durable conversation store,
remote MCP server, or R Shiny helper.

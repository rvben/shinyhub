# ShinyHub agent tools for Python Shiny

Install `shinyhub-agent` from PyPI and pin the resolved version in your app.
Review the changelog before upgrading while the API is below `1.0`.
Browser WebMCP availability depends on the browser. The app remains responsible
for tool permissions and for checking consequential results.

The session adapter requires Shiny 1.8 or later. Shiny 1.9 support starts
with `shinyhub-agent` 0.5.4.

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
        ui.update_select("period", selected=args["period"])
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

`ResponsesChat` (also available as `OpenAIChat`) and `BedrockChat` append a
shared description of these formatting
capabilities to the app's `instructions` on every model request, including tool
follow-ups. Explicit app formatting preferences still take precedence. With
`AGUIChat`, configure the same formatting guidance on the hoster-owned endpoint;
the helper does not modify that agent's system prompt. Formatting guidance is
advisory; safe rendering does not depend on the model following it.

```python
import os
from shinyhub_agent import AGUIChat, BedrockChat, ResponsesChat, chat_dependency

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
    chat = ResponsesChat(
        api_key=os.environ["OPENAI_API_KEY"],
        instructions="You help with this dashboard. Use registered tools for app facts.",
    )
register(session=session, input=input, tools=tools, chat=chat)
```

Pass `notice=` to `register()` to display an app-owned notice below the chat
header, separate from assistant messages:

```python
register(
    session=session, input=input, tools=tools, chat=chat,
    notice="The assistant uses generative AI and can make mistakes. "
           "Check important figures against the charts before you act on them.",
)
```

The notice stays visible above the conversation in native and fallback panels,
including after **New chat** and closing/reopening the panel. It is plain text:
HTML and Markdown appear literally. It is never added to model requests,
conversation history, or `UsageRecord` token counts. Long notices scroll within
their own keyboard-accessible area to leave room for the conversation and
composer. The question field references the notice for screen readers. Short
screens use a compact layout when a notice is configured.

`notice` accepts a string of at most 1,000 characters or `None` (the default).
Empty or whitespace-only strings behave like `None`; nonblank strings retain
their whitespace. Without a notice the existing panel is unchanged. Configure
it per viewer alongside the app's existing chat access gate. It has no effect
when `chat` is absent.

Install `shinyhub-agent[bedrock]` for `BedrockChat`. It uses Bedrock's
`ConverseStream` API and the standard AWS credential chain. Give the app only
`bedrock:InvokeModelWithResponseStream` for its chosen model or inference
profile. The model must support streaming tool use. On an on-premises ShinyHub
host, store AWS
credentials as private per-app secrets; on AWS, prefer a scoped workload role.
Choose a model ID available in the selected region. The adapter does not infer
one because model and tool support vary by region. Bedrock requests inherit the
AWS account's invocation logging and data policies.

Set `request_policy=` on `BedrockChat` to enforce an app's outbound data
policy. The synchronous callback receives an isolated copy of the complete
ConverseStream request, including the prompt, history, tool schemas and any
tool results. The adapter checks the request before each provider call,
including follow-up recovery and adapter retries. Only `True` authorizes a
request. Other results and exceptions block it before sending or emitting a
provider usage record. The AWS SDK may also retry an already approved request.
Apply field and value restrictions when constructing
model-visible data, and use the callback to check the assembled request.
Enforcement against app code requires a trusted provider route that owns the
credentials and applies its own policy.

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
`undo(args, result)` handler. By default, `undo_timeout=None` keeps Undo
available until the next write or chat/session reset.
Set `register(..., undo_timeout=3600)` to limit the window to an hour.
When a configured window ends, the button is removed and the receipt says
"Undo no longer available". Undo
runs in the same viewer's Shiny session without asking the model; the handler
must verify the app is still in the state created by that action before
restoring the previous state. Only the most recent write remains undoable.
Raise `ToolError("stale_view", "The date range changed again.")` for an expected
failure; the chat shows that message to the visitor and consumes the undo.
Unexpected failures show a generic message and are logged with a traceback.

To withdraw Undo as soon as relevant app state changes, add a synchronous,
read-only `undo_valid(args, result) -> bool` callback to the tool:

```python
def undo_still_valid(args, result):
    return selected_period.get() == result["period"]
```

Pass `undo_valid=undo_still_valid` alongside the tool's `undo` handler.
The callback runs after the chat receipt is sent, and again whenever a Shiny
input, reactive value or calculation it reads is invalidated. It runs in the
viewer's session with dependency tracking enabled. Read the server-owned state
updated by the tool or a reactive calculation derived from it. On Shiny 1.9
and later, separate effects may still be running when this check runs.
Returning `False` permanently consumes that undo and removes its button;
changing the view back does not revive it. A `ToolError` withdraws it with the
safe message, while an unexpected exception or a non-boolean return withdraws
it with generic text and logs the failure. The callback receives copies of
the original arguments and result. Only the most recent action is monitored;
reset, undo, replacement, configured expiry and session end clean up the monitor.
Monitoring survives stopping an answer after its receipt appears.

Return a built-in `bool`; wrap comparisons that return NumPy/Pandas boolean
scalars in `bool(...)`. Reading an unset input or failing Shiny's `req()`
quietly withdraws Undo, rather than waiting for the input to become available.
A replacement write can invalidate the previous action while it is applying,
so its old receipt may show withdrawal before the new receipt arrives. This
keeps the old Undo accurate even if the replacement write fails after changing
state.

Use normalized, server-owned state such as `selected_period` for the check.
A pending `ui.update_*()` can leave browser inputs at their old values during
the initial check. Do not poll or wait for an input echo inside this callback.
Keep the stale-state check in `undo` as well: the view can change between
clicking Undo and execution. The hook is optional and applies to registered
Shiny chat tools; it is not sent to the model or used by direct browser tools.

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

## Starter questions and follow-ups

Both settings are optional and belong to `register()`:

```python
register(
    session=session,
    input=input,
    tools=tools,
    chat=chat,
    starters=[
        "How did spend change versus the previous period?",
        "Which business lines cost the most?",
        "What will this month cost at the current run rate?",
    ],
    followups=2,
)
```

`starters=None` preserves the two built-in questions; `starters=[]` hides them.
Supply a sequence of at most four strings, each 1–120 characters after trimming.
Blank entries, line breaks, control characters, HTML tags, and duplicates
(case-insensitive after trimming) raise at registration. Starters are rendered
as literal text and keep the existing submit-on-click behaviour. They are panel
configuration, not model instructions.

Follow-ups are **experimental** until verified with your intended model and gateway.
`followups=0` (the default) disables them. Integers 1–3 enable optional suggestions in `BedrockChat` and `OpenAIChat` / `ResponsesChat`. The setting uses
a session-specific adapter copy, so sharing a chat adapter between viewers is
safe. `AGUIChat` does not currently support this setting; enabling it with an
unsupported agent raises at registration rather than silently doing nothing.

The adapters expose `suggest_followups` as a metadata tool and instruct the model
to offer follow-ups only through that channel. A successfully completed model
response with nonblank answer text and exactly one suggestion call ends the turn
without a tool result or another model request. Suggestion calls do not consume
app-tool budgets; their model tokens and request still appear in usage records.
Suggestions are best-effort: a model may return none. If a model returns only
suggestion calls without an answer, the adapter acknowledges them and makes at
most one additional request to recover the answer. Invalid or repeated suggestions
are dropped without preventing that recovery. Recovery never executes app tools
or retries again; a completed answer may include new suggestion calls, which are
ignored in favor of the original questions. Empty, incomplete, or app-tool-calling
recovery responses fail the turn without suggestions. Bedrock keeps native tool
results and tool configuration for recovery because Converse has no portable
"none" tool choice; requested app calls are rejected before execution. OpenAI sets
`tool_choice="none"` and also checks the response in case a gateway ignores it.
Recovery does not consume app-tool budgets; its actual request and tokens are
recorded in usage. The budget-ending response keeps all tools disabled.
There is no guaranteed latency or token saving. Answer text and suggestion arguments
share `max_output_tokens` (500 by default). Leave headroom for both: reaching the
output-token limit fails the turn even if answer text has already streamed, and
no suggestions are shown. Incomplete provider responses are never promoted to
successful answers.

If suggestions appear beside app tools, they are discarded from both execution
and the replayed tool transcript. Repeated suggestion calls are also discarded; without answer text they trigger
the same single recovery request, with a result for each call.
Malformed suggestion JSON or invalid questions are dropped without losing a valid
answer, within the existing stream and payload safety limits. The server trims
questions, rejects blank, multiline, control-character, HTML-tag, and overlong
entries (100 characters), removes case-insensitive duplicates, and keeps at most
the configured count. None of this validates factual accuracy or relevance.

Buttons appear only when an answer completes. A click fills and focuses the
composer **without sending**. Edit the question before sending; existing write
approvals still apply. While the composer contains a draft, the buttons are
disabled with an explanation to prevent overwriting it. Suggestions clear on the
next submission, reset, reconnect, error, cancellation, or successful Undo. An
expired approval suppresses suggestions for the rest of that turn, even when the
assistant finishes an explanation. Suggestions are never stored in conversation
history; sending one creates an ordinary user message.

Custom agents can opt in by implementing `with_followups(maximum: int)`, returning
a session-specific agent configured for that limit. Its existing `run()` signature
does not change. Yield `{"type": "suggestions", "questions": [...]}` as metadata;
`ChatSession` holds and validates it, then emits it immediately before `done` only
after a successful, nonempty answer. An agent's `error` event is forwarded with its
original message and terminates the turn without suggestions or a completed reply
in history. Applied actions are still recorded when a reply is interrupted. Agents must not execute suggestions as app
tools or make extra model requests solely to generate them. The name
`suggest_followups` is reserved among registered app tools while enabled.

## Backend protocols and routing

Choose an adapter by the protocol exposed by your endpoint:

| Adapter | Protocol | Where routing happens |
| --- | --- | --- |
| `ResponsesChat` | OpenAI Responses API with streaming and function tools | The configured endpoint, such as OpenAI or a compatible gateway |
| `BedrockChat` | AWS Bedrock ConverseStream | Bedrock, using the configured model or inference profile |
| `AGUIChat` | AG-UI streaming agent endpoint | The externally hosted agent |

`ResponsesChat` and `OpenAIChat` are names for the **same class**, with identical
arguments and defaults. Existing `OpenAIChat` integrations continue to work;
there is no deprecation or migration requirement. `ResponsesChat` is the clearer
name when the endpoint is a gateway rather than OpenAI itself. It still defaults
to OpenAI's endpoint and an OpenAI model, so configure both for other services.

The package does not choose between providers, balance requests, or configure
fallbacks. Put those policies in your gateway or hosted agent. A separate
adapter per vendor is unnecessary when the endpoint implements a supported
protocol. Compatibility still requires the streaming events, tool calls and
usage shapes that the adapter consumes; a gateway's “OpenAI-compatible” label
alone is not sufficient. Chat Completions-only endpoints are not supported by
`ResponsesChat`.

Applications can also supply a custom backend implementing the exported
`ChatAgent` protocol to `register(chat=...)`. That is an extension interface,
not a built-in router. All built-in adapters validate app tool calls and retain
server-side write approval.

## Responses-compatible gateways (LiteLLM example)

`ResponsesChat` can use a gateway's Responses API with a server-side virtual key
and model alias:

```python
chat = ResponsesChat(
    api_key=os.environ["LITELLM_API_KEY"],
    base_url=os.environ["LITELLM_BASE_URL"],  # e.g. https://gateway.example/v1
    model=os.environ["LITELLM_MODEL"],
    instructions="Use app tools for facts.",
    provider="litellm",
    send_end_user=True,
    on_usage=record_usage,  # Callback example in Usage attribution below.
)
# Inside server(), using identity verified by the app's authentication layer:
register(session=session, input=input, tools=tools, chat=chat,
         usage_metadata={"username": viewer.username})
```

`base_url` defaults to `https://api.openai.com/v1`. Requests append `/responses`
after removing trailing slashes, preserving any path prefix. HTTPS is required,
except HTTP on exactly `localhost` or `127.0.0.1` for local development. URLs
with credentials, queries, or fragments are rejected. Redirects are not followed.
`provider` defaults to `openai`; the explicit label appears in usage records and
model-call spans. It identifies the gateway, while `model` holds its alias.

`send_end_user` defaults to `False`. Enabling it sends the server-provided
`usage_metadata["username"]` in the JSON `user` field on **every** model request,
including tool follow-ups and the final round. The app must verify that identity;
the library does not authenticate it. Missing or blank identity fails before
network access. Browser-supplied identity is ignored. No end-user header or
other metadata forwarding is added. Shared adapters isolate identity per turn.
This flag is intended for gateways that use `user` for end-user budgets. If
enabled with the default OpenAI endpoint, it sends that username to OpenAI too.

Recognized LiteLLM budget errors produce a fixed visitor message: “The assistant
budget is used up. Contact your administrator.” Upstream error text is never
displayed. Ordinary rate limits remain generic failures. The gateway enforces
budgets; usage records only observe calls, with unknown token counts left as
`None`. Configure end-user budgets separately from app-key budgets.

HTTPX uses environment configuration by default: `SSL_CERT_FILE` or
`SSL_CERT_DIR` for corporate CAs, `HTTPS_PROXY` for an egress proxy, and `NO_PROXY`
for gateway hosts that bypass it. See [HTTPX environment variables](https://www.python-httpx.org/environment_variables/).
No custom HTTP client or header-forwarding option is provided.
For local HTTP endpoints, set `NO_PROXY=localhost,127.0.0.1` so proxy environment
settings do not send the virtual key and end-user identity through a proxy over
plain HTTP.

LiteLLM documents a [Responses API bridge](https://docs.litellm.ai/docs/response_api)
for other providers. Verify the deployed LiteLLM version and Bedrock model alias
before rollout: function-call IDs and results, streaming usage, approval and
denial, `store: false`, `parallel_tool_calls: false`, and the tools-disabled
final round must survive that bridge. Gateway spend logs must also show the
expected end user. Zero-budget rejection semantics can vary by gateway version;
explicitly verify that a zero-budget user is blocked before their first call.

An opt-in live smoke test exercises local read/write tools against your gateway.
Set `LITELLM_BASE_URL`, `LITELLM_API_KEY`, `LITELLM_MODEL`,
`LITELLM_TEST_USER` (allowed), and `LITELLM_BLOCKED_TEST_USER` (already exhausted).
Also set `LITELLM_ZERO_BUDGET_TEST_USER` to a distinct, freshly configured user
with zero spend and a zero budget. All three users must differ. A gateway that
allows the zero-budget user's first request fails this rollout check.
It makes billable requests but does not configure gateway users or budgets.
From this package's directory, in its test environment, run:

```sh
SHINYHUB_AGENT_LIVE_GATEWAY=1 PYTHONPATH=src python -m pytest tests/test_gateway_live.py -q
```

These tests are skipped by default. Passing mocked tests does not establish
compatibility with a particular live gateway deployment.

The default suite also uses a local HTTP contract server to exercise the real
HTTPX transport, multiline SSE, split UTF-8, tools, approvals, denial, usage and
budget isolation. This verifies client behavior without contacting a provider.
For a rollout, additionally record the deployed LiteLLM version and model alias,
run the live tests, and inspect gateway spend logs for those test users. The
live tests check the app-side records; they do not query gateway administration
APIs or establish spend-log attribution by themselves.

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

The chat history exists only in the viewer's Shiny session and defaults to
the last six exchanges (`history_exchanges` in `register()`). A new chat clears it. The app currently has no durable
conversation store. The helper does not provide a remote MCP server or a
platform-wide agent registry, administration UI, or billing controls.

## Approval and validation

JSON Schema validation runs before a chat approval card or an opted-in browser
confirmation. For constraints that depend on current app state, add a
side-effect-free `validate(args)` callback. It may be synchronous or async,
must return `None` on success, and can raise `ToolError(code, message)` with a
visitor-safe explanation. Validation runs again immediately before execution:
available data may change while a visitor considers an action. Read tools can
also have validators. Unexpected callback errors are sanitised.

An optional synchronous `describe(args)` gives a write an app-specific summary:

```python
# Inside server(), alongside selected_period and set_period above:
from shinyhub_agent import ToolError

available_periods = reactive.value(["week", "year"])

def validate_period(args):
    if args["period"] not in available_periods.get():
        raise ToolError("no_data", "That reporting period has no data yet.")

set_period_tool = AgentTool(
    "set_period", "Change the dashboard period",
    {"type": "object", "properties": {"period": {"type": "string"}},
     "required": ["period"], "additionalProperties": False},
    set_period, read_only=False, confirmation="Change the dashboard period?",
    validate=validate_period,
    describe=lambda args: f"Reporting period = {args['period']}",
)
```

Descriptions contain 1–300 characters; invalid descriptions fall back to the
argument display. Callbacks receive snapshots and cannot silently rewrite the
approved arguments. The chat shows nested keys and array values as plain text;
long summaries and custom descriptions retain expandable complete JSON details.
HTML is always literal. Approval has a visible deadline; expired cards remove
their buttons. Ask again to propose a fresh action.

## Shiny state and write results

Registered handlers, validators, descriptions and undo callbacks run in the
viewer session with reactive reads isolated. They can read `input.x()` or a
reactive calculation, call `ui.update_*()` without `session=`, and set reactive
values without a manual `reactive.flush()`. Model requests and approval waits
run independently of tool callbacks. Session end cancels outstanding requests.

On Shiny 1.9 and later, tool callbacks in the same viewer session run one at a
time. Shiny schedules dependent effects and outputs, which can run during an
`await` or after the callback returns. Keep state reads and writes that must
happen together in a section without an `await`. On Shiny 1.8, callbacks hold
Shiny's process-wide graph lock and flush pending work before returning.

`ui.update_*()` sends a client update. Read the updated `input.x()` only after
the browser echoes the value. Report the normalized values you sent in tool
and undo results, and keep callbacks short.
Return normalized, server-owned view state, as `selected_period`
does above. Route manual input changes through the same normalization function,
including clamping dates and clearing incompatible filters. Use that state for
subsequent reads and outputs. A browser acknowledgment cannot guarantee that
later app effects or asynchronous work have completed. This package provides
no app-wide `settle()` guarantee.

These guarantees apply to tools returned by `register()`. A standalone
`ToolRegistry` has validation and payload bounds but no Shiny execution context.
Writes are not transactional: a handler that raises after a mutation must
handle its own recovery. Undo must still check that the applied state is current.

## Budgets and limits

All backends execute calls in order. When a per-step budget or the
one-write-per-step rule defers a call, all later calls in that step are deferred
too. Each receives a correlated result with the `tool_deferred` error code;
the model can request that work in another step. Malformed or incomplete
streams never trigger app tools.

When the tool-round or per-turn call budget is exhausted, one final call with
app tools disabled answers from completed results. If it fails or still
requests tools, a deterministic response lists completed reads, applied actions
and recent safe tool errors. The final call is additional to `max_tool_rounds`
and is included in usage records. Suggestion-only answer recovery is terminal
and distinct from this budget fallback; recovery failures propagate as errors.

| Setting | Where | Default | Allowed range |
| --- | --- | ---: | --- |
| `max_tool_calls_per_step` | Any backend | 2 | 1–8 |
| `max_tool_calls_per_turn` | Any backend | 8 | 1–64 |
| `max_tool_rounds` | Any backend | 4 | 1–8, plus one final call |
| `max_output_tokens` | OpenAI/Bedrock | 500 | 100–8192; provider limits also apply |
| `approval_timeout` | `register()` | 30 s | 1–300 s |
| `undo_timeout` | `register()` | `None` | Finite seconds ≥1, or `None` for no time limit |
| `history_exchanges` | `register()` | 6 | 1–32 complete exchanges |
| `max_answer_chars` | `register()` | 8000 | 1000–32768 characters |

History drops the oldest complete exchanges when full. Backends retain the
registered history without a second six-exchange or 2000-character truncation.
Increase `max_answer_chars` when raising model output limits. Larger histories
and budgets increase token use.

Fixed bounds: 32 tools; 8192 UTF-8 bytes per schema or argument object; 32768
UTF-8 bytes per tool or undo result; 500 characters per tool description;
300 per confirmation; 140 per receipt; 1,000 per panel notice. Tool execution, preflight and undo have
an 8-second timeout including waiting to run. Streams allow
64 KiB per event and 1 MiB per response. Chat questions are capped at 2000
characters, 10 per minute and 40 per session; reset does not reset those budgets.
Browser dispatch allows 20 requests per minute, with write preflight and
execution each consuming one request. Browser confirmation expires after 30
seconds; each dispatch waits up to 10 seconds. Only the last chat write is
undoable, with no time limit by default; set `undo_timeout` to limit its duration.

## Usage attribution

All backends accept `on_usage(record)`, a synchronous or async callback receiving
a frozen `UsageRecord` per provider request, including final calls, failures
and cancellations. Records include UTC timestamp, provider, app, optional
username, model, turn/call IDs, one-based round, final-call flag, tokens, cache
tokens when available, duration in milliseconds, requested tool names and
outcome. The final-call flag identifies budget-exhaustion calls; suggestion-only
answer recovery uses the next round number with `final=False`. Unknown counts are `None`, not zero. AG-UI records describe endpoint
requests (`model="external"`); its protocol does not supply model token usage.
Account for its internal model calls on the endpoint.

```python
from dataclasses import asdict
import logging

usage_log = logging.getLogger("app.assistant_usage")

def record_usage(record):
    usage_log.info("assistant_usage", extra={"usage": asdict(record)})

chat = BedrockChat(
    model_id=os.environ["SHINYHUB_AGENT_BEDROCK_MODEL_ID"],
    region=os.environ["AWS_REGION"], instructions="Use app tools for facts.",
    on_usage=record_usage,
)

# Inside server(), after resolving viewer from verified session identity:
register(session=session, input=input, tools=tools, chat=chat,
         usage_metadata={"username": viewer.username if viewer else None})
```

App slug defaults from `SHINYHUB_APP_SLUG`. `usage_metadata` may set `app` and
`username` (up to 256 characters each). Usage callbacks receive the configured
username. If an app logs the full record in its callback, that log contains
personal data. The built-in logs and model-call spans omit the username. With
`ResponsesChat(send_end_user=True)` (or `OpenAIChat` with the same flag), the
username also leaves the app in the
Responses API JSON `user` field, as described above. Other usage metadata is
not forwarded. Resolve "me" in session-scoped tools.
Shared adapters keep usage context separate for each viewer and turn.

`shinyhub_agent._usage` also emits single-line JSON at INFO level with structured
`agent_usage` logging metadata. Where OpenTelemetry is installed, model-call
spans receive `shinyhub.agent.*` attributes. Traces may be sampled; use logs or
callbacks for complete accounting. Async callbacks have a two-second timeout;
keep callbacks short and enqueue durable accounting separately. Callback and
tracing failures do not fail answers. Records are observations, not exactly-once
billing events; use call IDs for deduplication.

## Opening chat in tests

The public `window.shinyhubAgentChat.open()` and `.close()` methods are
idempotent. They return `true` when a chat session is available, or `false`
before connection. They work with the native closed shadow root and fallback:

```javascript
await page.waitForFunction(() => window.shinyhubAgentChat?.open());
await page.locator(".sh-agent-input").fill("What is the current reporting period?");
await page.locator(".sh-agent-send").click();
```

The existing `shinyhub:chat:toggle` event with `{version: 1}` also works, but
toggles instead of ensuring the panel is open. Neither hook submits questions
or approves changes. The toolbar's own controls require separate toolbar tests.

From the repository root, `make test-py-agent` runs the Python and browser unit
tests. `make test-browser-agent-chat-e2e` checks native and fallback panels at
desktop and mobile sizes, including keyboard access, accessibility and plain
text argument rendering. `make test-browser-agent-shiny-e2e` builds and installs
the wheel, then exercises approval, updates, output recomputation, undo and
cancellation in a real Shiny session. `make test-browser-agent-shiny-compat`
runs that flow with the same wheel on Shiny 1.8.0 and 1.9.0. These checks use
Chrome by default and make no model provider requests.

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

### Real-model follow-up compatibility checks

The deterministic adapter tests verify protocol handling and request counts.
Before enabling follow-ups with a new model or gateway, run the opt-in, billable
probes against that deployment. They use synthetic dashboard facts and the default
500-token output limit. They check ordinary instructions (suggestions optional),
a forced text-plus-suggestion response, and a read-tool round followed by an answer
and suggestion. A metadata-only response may add one answer-recovery request;
text plus suggestions must keep the fast path. Questions must not repeat in the
answer/history:

```sh
# Configure only the backend being tested; normal AWS credentials apply to Bedrock.
export SHINYHUB_AGENT_BEDROCK_MODEL="your-model-id"
export AWS_REGION="your-region"
# Or configure OPENAI_API_KEY and SHINYHUB_AGENT_OPENAI_MODEL.
# Gateways can also set SHINYHUB_AGENT_OPENAI_BASE_URL and
# SHINYHUB_AGENT_OPENAI_PROVIDER (for example, "litellm").
SHINYHUB_AGENT_LIVE_FOLLOWUPS=1 uv run --extra test --extra bedrock \
  pytest tests/test_suggestions_live.py -q
```

Run this for each intended model. A passing probe establishes basic compatibility,
not suggestion quality or a latency improvement. Application evaluations should
also check relevance, self-contained wording, injected-data handling, and latency
against the same prompts with follow-ups disabled.

## Inline charts (experimental)

Charts are experimental and disabled by default. Enable them per viewer with
`register(..., charts=True)`. A read-only app tool
can return `ChartData` instead of an ordinary JSON result:

```python
from shinyhub_agent import AgentTool, ChartData, register

async def revenue_data(args):
    # Apply the same authorization and filters as the dashboard before returning.
    return ChartData(
        rows=[
            {"month": "2026-01-01", "revenue_eur": 12000},
            {"month": "2026-02-01", "revenue_eur": 19000},
        ],
        fields={"month": "temporal", "revenue_eur": "quantitative"},
        title="Illustrative monthly revenue (EUR)",
    )

# Inside server(), with chat_dependency() included in the UI:
register(session=session, input=input, chat=chat, charts=True, tools=[
    AgentTool("get_revenue", "Read monthly revenue for questions and charts", {
        "type": "object", "properties": {}, "additionalProperties": False,
    }, revenue_data),
])
```

The chat agent receives an opaque dataset ID, field types, row count, up to three
sample rows, and numeric minima/maxima. The full dataset stays in the app's
Shiny session until sent to that viewer's browser for display. Use an ordinary
read tool for additional analysis or exact aggregates: a preview is not the
whole dataset. Existing tool-result disclosure rules apply to the samples and
statistics sent to the model or hoster-owned endpoint.

The chat-only `render_chart` tool accepts a dataset ID, `kind` (`bar`, `line`,
`area`, `scatter`), `x`, `y`, optional categorical `color`, and `title`. It works
with ResponsesChat, BedrockChat, AGUIChat, and custom agents that execute the
provided tool registry. It is not exposed through WebMCP. Rendering a chart
needs no write approval and does not change dashboard filters. Registered
write tools keep their existing approval and Undo behavior.

Each card supports hover details, **Expand** (Escape closes it), **Download SVG**,
and **View data** with the complete source table, loaded on demand in 50-row
pages. Failed or stalled assets offer **Retry chart** and preserve source data;
asset loads time out after 10 seconds and render attempts after 20 seconds.
The browser retains the latest eight interactive charts, releasing older views
and replacing them with a message inviting the viewer to recreate the chart. Charts are snapshots; subsequent dashboard changes do not
rewrite old answers. The agent receives chart metadata in conversation history
so it can reuse a dataset for follow-up chart requests. New data requires a new
app read. Dataset IDs are session-local, limited to eight snapshots; evicted IDs
return a safe error instructing the agent to read again. New chat and successful
Undo clear dataset storage. Historical charts remain labeled snapshots after Undo.

Datasets accept 1–2,000 rows, 1–16 explicitly typed fields, and at most 256 KiB
of row JSON. Types are `nominal` (strings), `quantitative` (finite numbers), and
`temporal` (ISO calendar dates or timestamps using `T`); values may be null.
Zone-free timestamps are interpreted as UTC. Charts with no complete x/y points
are rejected, as are duplicate timestamps representing the same instant.
Integers must fit the browser safe range (±9,007,199,254,740,991). Preview rows
are omitted as needed to keep model tool results within the existing 32 KiB limit. Rows must exactly match
the declared fields. Aggregate larger datasets in app-owned code first. Chart
requests allow four cards per answer and twelve color groups. Non-scatter charts
require a single row per x value and group: the helper rejects ambiguous duplicate
points rather than silently aggregating them. Line/area charts require a numeric
or temporal x axis; scatter needs two numeric axes; y is always numeric. Bar/area
series are not stacked. Choose meaningful units in field names and chart titles.
Existing tool-call budgets and argument limits still apply.

The renderer constructs a fixed Vega-Lite specification from validated options.
Models cannot provide JavaScript, raw Vega specs, expressions, HTML, data URLs,
external data sources, or renderer settings. Pinned Vega libraries ship with the
package and load from the app's own asset path only when the first chart arrives;
no CDN or external chart service is used. Dependencies use an isolated namespace
and bypass host AMD/CommonJS loaders, preserving the app's own Vega libraries.
The default Vega runtime needs a CSP that permits its expression compilation;
when deployment CSP blocks rendering, source data remains available. The browser
checks verify this fallback without changing the deployment's security policy.
See `www/vendor/README.md` for versions and licenses. Without `charts=True`, existing tool behavior is retained; a
`ChartData` result becomes ordinary JSON subject to the usual 32 KiB result limit.

Chart verification:

```sh
# Real renderer, desktop/mobile, native/fallback, accessibility including contrast.
# Run each engine: chromium (Chrome), firefox, webkit (Safari engine).
SHINYHUB_E2E_BROWSER=chromium node scripts/agent-charts-browser-e2e.mjs

# Opt-in, billable model acceptance with synthetic values only.
# responses: OPENAI_API_KEY, SHINYHUB_AGENT_OPENAI_MODEL, optional SHINYHUB_AGENT_OPENAI_BASE_URL
# openrouter: OPENROUTER_API_KEY, OPENROUTER_MODEL; use SHINYHUB_AGENT_LIVE_CHARTS=openrouter
# bedrock: AWS credentials, AWS_REGION, SHINYHUB_AGENT_BEDROCK_MODEL
# agui: SHINYHUB_AGENT_AGUI_URL, optional SHINYHUB_AGENT_AGUI_TOKEN
SHINYHUB_AGENT_LIVE_CHARTS=responses pytest packaging/python-agent/tests/test_charts_live.py -q
```

The live probe checks data reads, chart creation, follow-up dataset reuse, and
reset. Run it against the intended deployment model before rollout. AG-UI
endpoints must themselves follow the chart tools supplied in each request.
## Releasing this package

Use a vership build with independent project support. The repository root
registers `agent`; its configuration lives beside this package's `pyproject.toml`.
From the repository root:

```sh
vership --project agent status
vership --project agent changelog patch
vership --project agent bump patch --dry-run
vership --project agent bump patch --prepare
```

Review the prepared commit and authorize publication before running
`vership --project agent release`. Wait for the `publish shinyhub-agent` workflow
to finish, then run `vership --project agent verify`. This checks the
`shinyhub-agent-v<version>` tag and the `shinyhub-agent` PyPI distribution.

The manifest and `shinyhub_agent.__version__` move together. This release does
not bump the ShinyHub server or its companion packages. If HEAD changes after
preparation, review the new content and run
`vership --project agent release --prepare` to refresh the same version before
releasing; do not bump it again.
